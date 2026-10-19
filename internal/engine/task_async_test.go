package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

type detachedTestRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r detachedTestRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	close(r.started)
	select {
	case <-r.release:
		return agent.AgentResult{Output: "async-child-result"}
	case <-ctx.Done():
		return agent.AgentResult{Error: ctx.Err().Error(), NoRetry: true}
	}
}

func parseAsyncReceipt(t *testing.T, req cpanthropic.MessageRequest) (string, string) {
	t.Helper()
	texts := backgroundResultTexts(req.Messages, "spawn")
	if len(texts) != 1 || !strings.Contains(texts[0], "status: submitted") || strings.Contains(texts[0], "running_in_background") {
		t.Fatalf("not a real submission receipt: %v", texts)
	}
	var id, run string
	for _, line := range strings.Split(texts[0], "\n") {
		if strings.HasPrefix(line, "agent_id: ") {
			id = strings.TrimPrefix(line, "agent_id: ")
		}
		if strings.HasPrefix(line, "run_id: ") {
			run = strings.TrimPrefix(line, "run_id: ")
		}
	}
	if id == "" || run == "" {
		t.Fatal("submission did not include stable ids")
	}
	return id, run
}

func TestAsyncTaskMainContinuesThenExplicitlyWaits(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "background disabled", true: "background enabled"}[background], func(t *testing.T) {
			runner := detachedTestRunner{started: make(chan struct{}), release: make(chan struct{})}
			c := agent.NewCoordinator(runner)
			t.Cleanup(c.Close)
			registry := tool.NewRegistry()
			independent := false
			registry.Register(tool.NewTaskTool(c), &backgroundTestTool{name: "Independent", run: func(context.Context) *tool.ContentBlock {
				<-runner.started
				independent = true
				close(runner.release)
				return tool.Result("independent-done")
			}})
			calls := 0
			client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				calls++
				switch calls {
				case 1:
					return backgroundMessage(t, backgroundCall("spawn", "Task", map[string]any{"action": "spawn", "description": "analyze", "prompt": "work", "run_in_background": true})), nil
				case 2:
					id, run := parseAsyncReceipt(t, req)
					return backgroundMessage(t, backgroundCall("independent", "Independent", map[string]any{}), backgroundCall("wait", "Task", map[string]any{"action": "wait", "agent_id": id, "run_id": run, "timeout_ms": 2000, "run_in_background": true})), nil
				case 3:
					results := backgroundResultTexts(req.Messages, "wait")
					if !independent || len(results) != 1 || !strings.Contains(results[0], "async-child-result") || strings.Contains(results[0], "running_in_background") {
						t.Fatalf("explicit wait did not return final result: %v", results)
					}
					if !messagesContain(req.Messages, "<background-task-notification>") {
						t.Error("completion mailbox not injected")
					}
					return backgroundMessage(t), nil
				default:
					t.Fatalf("unexpected model polling: %d", calls)
					return nil, nil
				}
			})
			eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 5, Permissions: permission.NewServiceWithConfig(permission.Config{Mode: permission.ModeBypass, BypassSubagents: true}), BackgroundTasksEnabled: background, BackgroundYieldAfter: time.Nanosecond})
			result := eng.Run(t.Context(), agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: "work"})
			if result.Error != "" || calls != 3 {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
		})
	}
}

func TestAsyncTaskSurvivesNormalTurnAndDeliversNextTurn(t *testing.T) {
	runner := detachedTestRunner{started: make(chan struct{}), release: make(chan struct{})}
	c := agent.NewCoordinator(runner)
	t.Cleanup(c.Close)
	registry := tool.NewRegistry()
	registry.Register(tool.NewTaskTool(c))
	var id, run string
	calls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if calls == 1 {
			return backgroundMessage(t, backgroundCall("spawn", "Task", map[string]any{"action": "spawn", "description": "analyze", "prompt": "work"})), nil
		}
		id, run = parseAsyncReceipt(t, req)
		return backgroundMessage(t), nil
	})
	cfg := Config{Client: client, Tools: registry, Permissions: permission.NewServiceWithConfig(permission.Config{Mode: permission.ModeBypass, BypassSubagents: true}), MaxTurns: 3}
	eng := NewEngine(cfg)
	ctx, cancel := context.WithCancel(t.Context())
	request := RunRequest{SessionID: "parent-session", AgentConfig: agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: "submit"}}
	first := eng.RunWithHistory(ctx, request)
	cancel()
	if first.AgentResult.Error != "" || calls != 2 {
		t.Fatalf("normal turn unexpectedly waited: %+v calls=%d", first.AgentResult, calls)
	}
	<-runner.started
	short, stop := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer stop()
	if result, err := c.WaitRun(short, agent.AgentID(id), run); err != context.DeadlineExceeded {
		t.Fatalf("parent end cancelled child: %+v %v", result, err)
	}
	// Config updates are safe while a detached run is active. The next main run
	// observes the new config while the child keeps its original run snapshot.
	cfg.Client = backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		if !messagesContain(req.Messages, "async-child-result") || contextOccurrences(req.Messages, "<background-task-notification>") != 1 {
			t.Error("next-turn completion missing or duplicated")
		}
		return backgroundMessage(t), nil
	})
	eng.UpdateConfig(cfg)
	close(runner.release)
	if result, err := c.WaitRun(t.Context(), agent.AgentID(id), run); err != nil || result.Error != "" {
		t.Fatalf("child did not finish: %+v %v", result, err)
	}
	request.Messages = first.Messages
	request.AgentConfig.Prompt = "continue"
	if second := eng.RunWithHistory(t.Context(), request); second.AgentResult.Error != "" {
		t.Fatal(second.AgentResult.Error)
	}
}

func TestAsyncTaskStillChecksRevokedPermissions(t *testing.T) {
	registry := tool.NewRegistry()
	mutations := 0
	registry.Register(&backgroundTestTool{name: "Mutate", run: func(context.Context) *tool.ContentBlock { mutations++; return tool.Result("changed") }})
	started, release := make(chan struct{}), make(chan struct{})
	permissions := permission.NewServiceWithConfig(permission.Config{Mode: permission.ModeBypass, BypassSubagents: true})
	calls := 0
	client := backgroundTestClient(func(ctx context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if calls == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return backgroundMessage(t, backgroundCall("mutate", "Mutate", map[string]any{})), nil
		}
		results := backgroundResultTexts(req.Messages, "mutate")
		if len(results) != 1 || !strings.Contains(results[0], "tool denied for session") {
			t.Errorf("revocation not applied: %v", results)
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, Permissions: permissions, ModelName: "test", MaxTurns: 3})
	coordinator := agent.NewCoordinator(eng)
	t.Cleanup(coordinator.Close)
	registry.Register(tool.NewTaskTool(coordinator))
	executor := NewToolExecutorWithPermissions(registry, nil, permissions)
	receipt := executor.Execute(t.Context(), ToolCall{Name: "Task", Input: []byte(`{"action":"spawn","description":"work","prompt":"work"}`)}, ToolEnv{UseContext: &tool.UseContext{WorkDir: t.TempDir(), SessionID: "main", PermissionMode: "bypass"}})
	if receipt.IsError || receipt.Content == nil || receipt.Content.AgentID == "" {
		t.Fatalf("failed submission: %+v", receipt)
	}
	<-started
	permissions.DenyTool("Mutate")
	close(release)
	result, err := coordinator.WaitRun(t.Context(), agent.AgentID(receipt.Content.AgentID), receipt.Content.RunID)
	if err != nil || result.Error != "" || mutations != 0 {
		t.Fatalf("revoked mutation ran: %+v err=%v mutations=%d", result, err, mutations)
	}
}

func TestAsyncRunsUseIndependentConfigSnapshots(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	client := backgroundTestClient(func(ctx context.Context, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
		close(started)
		select {
		case <-release:
			return backgroundMessage(t), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	cfg := Config{Client: client, Tools: tool.NewRegistry(), MaxTurns: 1, ModelName: "first"}
	eng := NewEngine(cfg)
	done := make(chan agent.AgentResult, 1)
	go func() {
		done <- eng.Run(t.Context(), agent.AgentConfig{ID: "child", Role: agent.AgentRoleTask, Prompt: "work"})
	}()
	<-started
	for i := 0; i < 100; i++ {
		cfg.ModelName = "updated"
		eng.UpdateConfig(cfg)
	}
	close(release)
	if result := <-done; result.Error != "" {
		t.Fatal(result.Error)
	}
}
