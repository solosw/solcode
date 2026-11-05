package engine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestParentQuestionRoundTripsWithParallelChildren(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, mode := range []string{"spawn", "legacy-graph"} {
		for _, background := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/background=%v", mode, background), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				reg := tool.NewRegistry()
				var c *agent.Coordinator
				var mainCalls, mainActive, childCalls atomic.Int32
				scope := agent.Scope{ParentID: "main", ParentSessionID: "session", WorkDir: t.TempDir()}
				client := backgroundTestClient(func(ctx context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
					if req.Model == "child" {
						childCalls.Add(1)
						name := ""
						for _, m := range req.Messages {
							for _, b := range m.Content {
								if b.OfText != nil && (b.OfText.Text == "work-A" || b.OfText.Text == "work-B") {
									name = b.OfText.Text
								}
							}
						}
						if name == "" {
							return nil, fmt.Errorf("child lost its own task")
						}
						first := backgroundResultTexts(req.Messages, "ask-"+name+"-1")
						second := backgroundResultTexts(req.Messages, "ask-"+name+"-2")
						if len(first) == 0 {
							return backgroundMessage(t, backgroundCall("ask-"+name+"-1", tool.AskParentToolName, map[string]any{"question": "decision-" + name + "-1", "context": "choose implementation"})), nil
						}
						if first[0] != "answer-decision-"+name+"-1" {
							return nil, fmt.Errorf("misrouted first answer: %v", first)
						}
						if len(second) == 0 {
							return backgroundMessage(t, backgroundCall("ask-"+name+"-2", tool.AskParentToolName, map[string]any{"question": "decision-" + name + "-2"})), nil
						}
						if second[0] != "answer-decision-"+name+"-2" {
							return nil, fmt.Errorf("misrouted second answer: %v", second)
						}
						return backgroundMessage(t), nil
					}
					if mainActive.Add(1) != 1 {
						t.Error("parent model executed concurrently")
					}
					defer mainActive.Add(-1)
					call := mainCalls.Add(1)
					if call == 1 {
						a := map[string]any{"id": "A", "agent_type": "explorer", "description": "A", "prompt": "work-A", "allowed_tools": []string{"View"}}
						b := map[string]any{"id": "B", "agent_type": "reviewer", "description": "B", "prompt": "work-B", "allowed_tools": []string{"View"}}
						if mode == "spawn" {
							a["action"] = "spawn"
							b["action"] = "spawn"
							return backgroundMessage(t, backgroundCall("spawn-A", tool.TaskToolName, a), backgroundCall("spawn-B", tool.TaskToolName, b)), nil
						}
						return backgroundMessage(t, backgroundCall("graph", tool.TaskToolName, map[string]any{"tasks": []any{a, b}, "execution_mode": "parallel"})), nil
					}
					questions, err := c.ParentQuestions(ctx, scope)
					if err != nil {
						return nil, err
					}
					if len(questions) > 0 {
						if !messagesContain(req.Messages, "status: needs_input") {
							t.Error("parent woke without a question notification")
						}
						var calls []map[string]any
						for _, q := range questions {
							calls = append(calls, backgroundCall("reply-"+q.ID, tool.ReplyChildToolName, map[string]any{"agent_id": q.AgentID, "run_id": q.RunID, "request_id": q.ID, "answer": "answer-" + q.Question}))
						}
						return backgroundMessage(t, calls...), nil
					}
					return backgroundMessage(t), nil
				})
				eng := NewEngine(Config{Client: client, Tools: reg, ModelName: "parent", SubagentModelName: "child", MaxTurns: 30, BackgroundTasksEnabled: background, BackgroundYieldAfter: time.Nanosecond, Permissions: permission.NewServiceWithConfig(permission.Config{Mode: permission.ModeBypass, BypassSubagents: true})})
				c = agent.NewCoordinator(eng)
				defer c.Close()
				reg.Register(tool.NewTaskTool(c), tool.NewAskParentTool(c), tool.NewReplyChildTool(c))
				result := eng.RunWithHistory(ctx, RunRequest{SessionID: "session", AgentConfig: agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, WorkDir: scope.WorkDir, Prompt: "coordinate"}})
				if result.AgentResult.Error != "" {
					t.Fatalf("collaboration failed: %+v", result.AgentResult)
				}
				records, err := c.Records(ctx, scope)
				if err != nil || len(records) != 2 {
					t.Fatalf("children=%d err=%v", len(records), err)
				}
				for _, r := range records {
					if r.Status.State != agent.AgentIdle || r.Result.Error != "" || len(r.ParentRequests) != 2 {
						t.Fatalf("child failed: status=%+v result=%+v requests=%d", r.Status, r.Result, len(r.ParentRequests))
					}
					history, err := c.History(ctx, r.Status.ID, scope)
					if err != nil || len(agent.PendingToolIDs(history.Messages)) != 0 {
						t.Fatalf("unpaired tool exchange: %v", err)
					}
				}
				if childCalls.Load() != 6 {
					t.Fatalf("child model polled or restarted: %d calls", childCalls.Load())
				}
				if mainCalls.Load() > 12 {
					t.Fatalf("unexpected parent polling: %d", mainCalls.Load())
				}
			})
		}
	}
}

func TestParentParksWithoutModelPollingAndWakesOnCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	runner := detachedTestRunner{started: make(chan struct{}), release: make(chan struct{})}
	coordinator := agent.NewCoordinator(runner)
	defer coordinator.Close()
	registry := tool.NewRegistry()
	registry.Register(tool.NewTaskTool(coordinator))
	waiting := make(chan struct{}, 1)
	var calls atomic.Int32
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		if calls.Add(1) == 1 {
			return backgroundMessage(t, backgroundCall("spawn", tool.TaskToolName, map[string]any{"action": "spawn", "description": "child", "prompt": "work"})), nil
		}
		if !messagesContain(req.Messages, "async-child-result") {
			t.Error("parent woke without completion")
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 4, Permissions: permission.NewServiceWithConfig(permission.Config{Mode: permission.ModeBypass, BypassSubagents: true}), OnStatus: func(s string) {
		if strings.Contains(s, "Waiting for child") {
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
	}})
	done := make(chan RunResult, 1)
	go func() {
		done <- eng.RunWithHistory(ctx, RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Prompt: "coordinate", WorkDir: t.TempDir()}})
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	<-runner.started
	if calls.Load() != 1 {
		t.Fatal("model requested while child had no events")
	}
	close(runner.release)
	select {
	case result := <-done:
		if result.AgentResult.Error != "" {
			t.Fatal(result.AgentResult.Error)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
