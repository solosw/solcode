package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

type backgroundTestClient func(context.Context, cpanthropic.MessageRequest) (*sdk.Message, error)

func (f backgroundTestClient) Create(ctx context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
	return f(ctx, req)
}

type backgroundTestTool struct {
	tool.BaseTool
	name string
	run  func(context.Context) *tool.ContentBlock
}

func (t *backgroundTestTool) Name() string                { return t.name }
func (t *backgroundTestTool) Description() string         { return "background test" }
func (t *backgroundTestTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *backgroundTestTool) Invoke(ctx context.Context, _ *tool.UseContext, _ json.RawMessage) (*tool.ContentBlock, error) {
	return t.run(ctx), nil
}

func backgroundMessage(t *testing.T, calls ...map[string]any) *sdk.Message {
	t.Helper()
	content := []any{map[string]any{"type": "text", "text": "all done"}}
	stop := "end_turn"
	if len(calls) > 0 {
		content = nil
		stop = "tool_use"
		for _, call := range calls {
			content = append(content, call)
		}
	}
	raw, err := json.Marshal(map[string]any{"id": "msg", "type": "message", "role": "assistant", "model": "test", "stop_reason": stop, "content": content})
	if err != nil {
		t.Fatal(err)
	}
	var msg sdk.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	return &msg
}

func backgroundCall(id, name string, input any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func backgroundResultTexts(messages []sdk.MessageParam, id string) []string {
	var texts []string
	for _, message := range messages {
		for _, block := range message.Content {
			if result := block.OfToolResult; result != nil && result.ToolUseID == id {
				for _, content := range result.Content {
					if content.OfText != nil {
						texts = append(texts, content.OfText.Text)
					}
				}
			}
		}
	}
	return texts
}

func TestBackgroundToolsLetMainAgentDoIndependentWork(t *testing.T) {
	for _, name := range []string{tool.BashToolName, tool.TaskToolName} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var longCalls atomic.Int32
			registry := tool.NewRegistry()
			registry.Register(&backgroundTestTool{name: name, run: func(ctx context.Context) *tool.ContentBlock {
				longCalls.Add(1)
				select {
				case <-release:
					return tool.Result("long work finished")
				case <-ctx.Done():
					return tool.ErrorResult(ctx.Err().Error())
				}
			}}, &backgroundTestTool{name: tool.ViewToolName, run: func(context.Context) *tool.ContentBlock {
				return tool.Result("independent work finished")
			}})
			calls, doneEvents := 0, 0
			client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				calls++
				switch calls {
				case 1:
					return backgroundMessage(t, backgroundCall("long", name, map[string]any{})), nil
				case 2:
					texts := backgroundResultTexts(req.Messages, "long")
					if len(texts) != 1 || !strings.Contains(texts[0], "running_in_background") {
						t.Errorf("background acknowledgement = %#v", texts)
					}
					if messagesContain(req.Messages, "<background-task-notification>") {
						t.Error("received completion before long work finished")
					}
					return backgroundMessage(t, backgroundCall("independent", tool.ViewToolName, map[string]any{})), nil
				case 3:
					if got := backgroundResultTexts(req.Messages, "independent"); len(got) != 1 || got[0] != "independent work finished" {
						t.Errorf("independent result = %#v", got)
					}
					unblock()
					return backgroundMessage(t), nil
				case 4:
					if !messagesContain(req.Messages, "long work finished") || !messagesContain(req.Messages, "status: completed") {
						t.Error("missing completion notification")
					}
					if len(backgroundResultTexts(req.Messages, "long")) != 1 {
						t.Error("completion must not produce a duplicate tool_result")
					}
					return backgroundMessage(t), nil
				default:
					return nil, fmt.Errorf("unexpected model polling: call %d", calls)
				}
			})
			eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 8, BackgroundTasksEnabled: true, BackgroundYieldAfter: 5 * time.Millisecond,
				OnToolDone: func(name, output string, isError bool, id string) {
					if id == "long" {
						doneEvents++
						if isError || output != "long work finished" {
							t.Errorf("UI completion = %q, error=%v", output, isError)
						}
					}
				},
			})
			result := eng.RunWithHistory(ctx, RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Prompt: "work", WorkDir: t.TempDir()}})
			if result.AgentResult.Error != "" || calls != 4 || longCalls.Load() != 1 || doneEvents != 1 {
				t.Fatalf("result = %#v; requests=%d executions=%d UI completions=%d", result.AgentResult, calls, longCalls.Load(), doneEvents)
			}
		})
	}
}

func TestBackgroundCompletionFailureIsDelivered(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	release := make(chan struct{})
	registry := tool.NewRegistry()
	registry.Register(&backgroundTestTool{name: tool.BashToolName, run: func(ctx context.Context) *tool.ContentBlock {
		select {
		case <-release:
			return tool.ErrorResult("exit code 7")
		case <-ctx.Done():
			return tool.ErrorResult(ctx.Err().Error())
		}
	}})
	calls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		switch calls {
		case 1:
			return backgroundMessage(t, backgroundCall("long", tool.BashToolName, map[string]any{"run_in_background": true})), nil
		case 2:
			close(release)
		case 3:
			if !messagesContain(req.Messages, "status: failed") || !messagesContain(req.Messages, "exit code 7") {
				t.Error("failure notification was not delivered")
			}
		default:
			return nil, fmt.Errorf("unexpected model call %d", calls)
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 5, BackgroundTasksEnabled: true, BackgroundYieldAfter: time.Hour})
	result := eng.Run(ctx, agent.AgentConfig{ID: "main", Prompt: "work"})
	if result.Error != "" || calls != 3 {
		t.Fatalf("result = %#v; requests=%d", result, calls)
	}
}

func TestBackgroundIdleRunAcceptsQueuedUserPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	release := make(chan struct{})
	registry := tool.NewRegistry()
	registry.Register(&backgroundTestTool{name: tool.BashToolName, run: func(ctx context.Context) *tool.ContentBlock {
		select {
		case <-release:
			return tool.Result("background finished")
		case <-ctx.Done():
			return tool.ErrorResult(ctx.Err().Error())
		}
	}})
	var queued atomic.Bool
	calls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		switch calls {
		case 1:
			return backgroundMessage(t, backgroundCall("long", tool.BashToolName, map[string]any{})), nil
		case 2:
			// Publish while the engine is idle, rather than during tool execution.
			time.AfterFunc(20*time.Millisecond, func() { queued.Store(true) })
		case 3:
			if !messagesContain(req.Messages, "new independent user work") {
				t.Error("queued prompt did not wake the idle run")
			}
			close(release)
		case 4:
			if !messagesContain(req.Messages, "background finished") {
				t.Error("missing background result")
			}
		default:
			return nil, fmt.Errorf("unexpected model call %d", calls)
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 6, BackgroundTasksEnabled: true, BackgroundYieldAfter: 5 * time.Millisecond,
		QueuedPrompts: func() []string {
			if queued.Swap(false) {
				return []string{"new independent user work"}
			}
			return nil
		},
	})
	result := eng.Run(ctx, agent.AgentConfig{ID: "main", Prompt: "work"})
	if result.Error != "" || calls != 4 {
		t.Fatalf("result = %#v; requests=%d", result, calls)
	}
}

func TestBackgroundCancellationAndTurnLimitCancelWorkers(t *testing.T) {
	for _, turnLimit := range []int{1, 5} {
		t.Run(fmt.Sprint(turnLimit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			workerStopped := make(chan struct{})
			registry := tool.NewRegistry()
			registry.Register(&backgroundTestTool{name: tool.TaskToolName, run: func(ctx context.Context) *tool.ContentBlock {
				<-ctx.Done()
				close(workerStopped)
				return tool.ErrorResult(ctx.Err().Error())
			}})
			calls := 0
			client := backgroundTestClient(func(_ context.Context, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
				calls++
				if calls == 1 {
					return backgroundMessage(t, backgroundCall("long", tool.TaskToolName, map[string]any{})), nil
				}
				cancel()
				return backgroundMessage(t), nil
			})
			eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: turnLimit, BackgroundTasksEnabled: true, BackgroundYieldAfter: 5 * time.Millisecond})
			result := eng.RunWithHistory(ctx, RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Prompt: "work"}})
			if result.AgentResult.Error == "" || !messagesContain(result.Messages, "status: canceled") {
				t.Fatalf("result = %#v; messages=%#v", result.AgentResult, result.Messages)
			}
			select {
			case <-workerStopped:
			case <-time.After(time.Second):
				t.Fatal("background worker was orphaned")
			}
		})
	}
}

func TestBackgroundSchedulingDisabledForNestedAgents(t *testing.T) {
	for _, role := range []agent.AgentRole{agent.AgentRoleTask, agent.AgentRoleSub} {
		t.Run(string(role), func(t *testing.T) {
			registry := tool.NewRegistry()
			registry.Register(&backgroundTestTool{name: tool.BashToolName, run: func(context.Context) *tool.ContentBlock {
				time.Sleep(10 * time.Millisecond)
				return tool.Result("synchronous child result")
			}})
			calls := 0
			client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				calls++
				if calls == 1 {
					return backgroundMessage(t, backgroundCall("child", tool.BashToolName, map[string]any{"run_in_background": true})), nil
				}
				texts := backgroundResultTexts(req.Messages, "child")
				if len(texts) != 1 || texts[0] != "synchronous child result" || messagesContain(req.Messages, "<background-task-notification>") {
					t.Errorf("child results = %#v", texts)
				}
				return backgroundMessage(t), nil
			})
			eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 4, BackgroundTasksEnabled: true, BackgroundYieldAfter: time.Millisecond})
			result := eng.Run(t.Context(), agent.AgentConfig{ID: "child", Role: role, Prompt: "work"})
			if result.Error != "" || calls != 2 {
				t.Fatalf("result = %#v; requests=%d", result, calls)
			}
		})
	}
}

func TestBackgroundSchedulerFastDisabledAndNonEligibleTools(t *testing.T) {
	for _, name := range []string{tool.BashToolName, tool.TaskToolName, tool.ViewToolName} {
		b := newBackgroundTools(true, time.Second)
		got := b.execute(t.Context(), name, "call", json.RawMessage(`{}`), func(_ context.Context, onStart func()) ToolResult {
			if onStart != nil {
				onStart()
			}
			return ToolResult{Content: tool.Result("fast result")}
		})
		if got.Background || b.hasPending() || got.Content.Text != "fast result" || len(b.drain(nil)) != 0 {
			t.Fatalf("%s result = %#v", name, got)
		}
	}
	b := newBackgroundTools(false, time.Millisecond)
	got := b.execute(t.Context(), tool.BashToolName, "call", json.RawMessage(`{"run_in_background":true}`), func(_ context.Context, onStart func()) ToolResult {
		if onStart != nil {
			onStart()
		}
		return ToolResult{Content: tool.Result("disabled")}
	})
	if got.Background || got.Content.Text != "disabled" {
		t.Fatalf("disabled result = %#v", got)
	}
}

func TestBackgroundNotificationsAreRunScoped(t *testing.T) {
	one, two := newBackgroundTools(true, time.Second), newBackgroundTools(true, time.Second)
	release := make(chan struct{})
	result := one.execute(t.Context(), tool.BashToolName, "call", json.RawMessage(`{"run_in_background":true}`), func(_ context.Context, onStart func()) ToolResult {
		if onStart != nil {
			onStart()
		}
		<-release
		return ToolResult{Content: tool.Result("only run one")}
	})
	if !result.Background {
		t.Fatal("explicit background did not yield")
	}
	close(release)
	<-one.pending[0].done
	if two.hasPending() || len(two.drain(nil)) != 0 {
		t.Fatal("result leaked into a different run")
	}
	if got := one.drain(nil); len(got) != 1 || !strings.Contains(got[0], "only run one") {
		t.Fatalf("notifications = %#v", got)
	}
	if got := one.drain(nil); len(got) != 0 {
		t.Fatalf("duplicate notification: %#v", got)
	}
}

type backgroundGraphRunner struct {
	t       *testing.T
	release <-chan struct{}
}

func (r backgroundGraphRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	if cfg.TaskID == "first" {
		select {
		case <-r.release:
			return agent.AgentResult{Output: "first-output"}
		case <-ctx.Done():
			return agent.AgentResult{Error: ctx.Err().Error()}
		}
	}
	if !strings.Contains(cfg.Prompt, "first-output") {
		r.t.Error("dependent task started without its predecessor's result")
	}
	return agent.AgentResult{Output: "second-output"}
}

func TestBackgroundTaskGraphPreservesDependencies(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	release := make(chan struct{})
	coordinator := agent.NewCoordinator(backgroundGraphRunner{t: t, release: release})
	registry := tool.NewRegistry()
	registry.Register(tool.NewTaskTool(coordinator))
	calls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		switch calls {
		case 1:
			return backgroundMessage(t, backgroundCall("graph", tool.TaskToolName, map[string]any{
				"run_in_background": true,
				"tasks": []map[string]any{
					{"id": "first", "description": "first", "prompt": "produce first output"},
					{"id": "second", "description": "second", "prompt": "use first output", "depends_on": []string{"first"}},
				},
			})), nil
		case 2:
			if texts := backgroundResultTexts(req.Messages, "graph"); len(texts) != 1 || !strings.Contains(texts[0], "running_in_background") {
				t.Errorf("graph acknowledgement = %#v", texts)
			}
			close(release)
		case 3:
			if !messagesContain(req.Messages, "first-output") || !messagesContain(req.Messages, "second-output") {
				t.Error("whole-graph completion was not delivered")
			}
		default:
			return nil, fmt.Errorf("unexpected model call %d", calls)
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 5, BackgroundTasksEnabled: true})
	result := eng.Run(ctx, agent.AgentConfig{ID: "main", Prompt: "do graph"})
	if result.Error != "" || calls != 3 {
		t.Fatalf("result = %#v; requests=%d", result, calls)
	}
}

func TestBackgroundSchedulerCapsDetachedCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := newBackgroundTools(true, time.Second)
	defer b.close()
	for i := 0; i < maxBackgroundTools; i++ {
		result := b.execute(ctx, tool.BashToolName, fmt.Sprint(i), json.RawMessage(`{"run_in_background":true}`), func(ctx context.Context, onStart func()) ToolResult {
			if onStart != nil {
				onStart()
			}
			<-ctx.Done()
			return ToolResult{Content: tool.ErrorResult("canceled"), IsError: true}
		})
		if !result.Background {
			t.Fatalf("job %d should detach", i)
		}
	}
	result := b.execute(ctx, tool.BashToolName, "over-capacity", json.RawMessage(`{"run_in_background":true}`), func(_ context.Context, onStart func()) ToolResult {
		if onStart != nil {
			onStart()
		}
		return ToolResult{Content: tool.Result("stayed foreground")}
	})
	if result.Background || result.Content.Text != "stayed foreground" || len(b.pending) != maxBackgroundTools {
		t.Fatalf("capacity was exceeded: %#v; pending=%d", result, len(b.pending))
	}
}

func TestBackgroundRealBashCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := "sleep 0.15 && printf actual-background-finished"
	if runtime.GOOS == "windows" {
		command = "ping -n 2 127.0.0.1 > nul && echo actual-background-finished"
	}
	registry := tool.NewRegistry()
	registry.Register(tool.NewBashTool())
	calls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		switch calls {
		case 1:
			return backgroundMessage(t, backgroundCall("shell", tool.BashToolName, map[string]any{"command": command, "timeout": 4000})), nil
		case 2:
			if texts := backgroundResultTexts(req.Messages, "shell"); len(texts) != 1 || !strings.Contains(texts[0], "running_in_background") {
				t.Errorf("real shell acknowledgement = %#v", texts)
			}
		case 3:
			if !messagesContain(req.Messages, "actual-background-finished") || !messagesContain(req.Messages, "status: completed") {
				t.Error("missing real shell completion output")
			}
		default:
			return nil, fmt.Errorf("unexpected model call %d", calls)
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 5, BackgroundTasksEnabled: true, BackgroundYieldAfter: 5 * time.Millisecond})
	result := eng.Run(ctx, agent.AgentConfig{ID: "main", Prompt: "run shell", WorkDir: t.TempDir()})
	if result.Error != "" || calls != 3 {
		t.Fatalf("result = %#v; requests=%d", result, calls)
	}
}

func TestBackgroundBashStillHonorsExecutionDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	command := "sleep 10"
	if runtime.GOOS == "windows" {
		command = "ping -n 12 127.0.0.1 > nul"
	}
	registry := tool.NewRegistry()
	registry.Register(tool.NewBashTool())
	calls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if calls == 1 {
			return backgroundMessage(t, backgroundCall("deadline", tool.BashToolName, map[string]any{
				"command": command, "timeout": 50, "run_in_background": true,
			})), nil
		}
		if calls == 3 && (!messagesContain(req.Messages, "Command timed out") || !messagesContain(req.Messages, "status: failed")) {
			t.Error("background command ignored its execution deadline")
		}
		if calls > 3 {
			return nil, fmt.Errorf("unexpected model call %d", calls)
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, MaxTurns: 5, BackgroundTasksEnabled: true})
	result := eng.Run(ctx, agent.AgentConfig{ID: "main", Prompt: "run shell", WorkDir: t.TempDir()})
	if result.Error != "" || calls != 3 {
		t.Fatalf("result = %#v; requests=%d", result, calls)
	}
}

type backgroundPermissionTool struct{ *backgroundTestTool }

func (*backgroundPermissionTool) IsDestructive(json.RawMessage) bool { return true }

func TestBackgroundYieldDoesNotBypassOrBackgroundPermissionDialog(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			approvalStarted, approvalRelease := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(approvalRelease) }) }
			defer release()
			permissions := permission.NewService(permission.ModeAuto)
			permissions.SetAskFunc(func(string, string) bool {
				close(approvalStarted)
				<-approvalRelease
				return false
			})
			registry := tool.NewRegistry()
			registry.Register(&backgroundPermissionTool{&backgroundTestTool{name: tool.BashToolName, run: func(context.Context) *tool.ContentBlock {
				t.Error("denied command was executed")
				return tool.Result("should not run")
			}}})
			var calls atomic.Int32
			client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				if calls.Add(1) == 1 {
					return backgroundMessage(t, backgroundCall("permission", tool.BashToolName, map[string]any{"run_in_background": explicit})), nil
				}
				texts := backgroundResultTexts(req.Messages, "permission")
				if len(texts) != 1 || strings.Contains(texts[0], "running_in_background") || !strings.Contains(texts[0], "denied") {
					t.Errorf("permission result = %#v", texts)
				}
				return backgroundMessage(t), nil
			})
			eng := NewEngine(Config{Client: client, Tools: registry, Permissions: permissions, MaxTurns: 4, BackgroundTasksEnabled: true, BackgroundYieldAfter: time.Millisecond})
			finished := make(chan agent.AgentResult, 1)
			go func() { finished <- eng.Run(ctx, agent.AgentConfig{ID: "main", Prompt: "run command"}) }()
			select {
			case <-approvalStarted:
			case <-ctx.Done():
				t.Fatal("permission dialog did not start")
			}
			time.Sleep(20 * time.Millisecond) // longer than the configured yield interval
			if calls.Load() != 1 {
				t.Error("model continued while permission was still pending")
			}
			release()
			select {
			case result := <-finished:
				if result.Error != "" || calls.Load() != 2 {
					t.Fatalf("result = %#v; requests=%d", result, calls.Load())
				}
			case <-ctx.Done():
				t.Fatal("main agent did not finish after denial")
			}
		})
	}
}
