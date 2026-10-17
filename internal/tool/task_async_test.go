package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
	"github.com/solosw/solcode/internal/tool"
)

type asyncGateRunner struct {
	started chan agent.AgentConfig
	release chan struct{}
}

func (r *asyncGateRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	select {
	case r.started <- cfg:
	case <-ctx.Done():
		return agent.AgentResult{Error: ctx.Err().Error(), NoRetry: true}
	}
	select {
	case <-r.release:
		return agent.AgentResult{Output: "finished " + cfg.Prompt}
	case <-ctx.Done():
		return agent.AgentResult{Error: ctx.Err().Error(), NoRetry: true}
	}
}

func waitAsyncResult(t *testing.T, task tool.Tool, uctx *tool.UseContext, receipt *tool.ContentBlock) *tool.ContentBlock {
	t.Helper()
	result := invokeTaskRaw(t, task, uctx, map[string]any{"action": "wait", "agent_id": receipt.AgentID, "run_id": receipt.RunID, "timeout_ms": 2000})
	requireTask(t, result)
	if strings.Contains(result.Text, `"timed_out":true`) {
		t.Fatalf("agent did not finish: %s", result.Text)
	}
	return result
}

func TestTaskAsyncSubmitWaitResumeAndPinnedRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &asyncGateRunner{started: make(chan agent.AgentConfig, 4), release: make(chan struct{}, 4)}
	storeDir, workDir := t.TempDir(), t.TempDir()
	c := agent.NewCoordinator(runner, agentsession.NewFileStore(storeDir))
	t.Cleanup(c.Close)
	task := tool.NewTaskTool(c)
	uctx := &tool.UseContext{WorkDir: workDir, SessionID: "parent", AgentID: "main", PermissionMode: "bypass"}
	ctx, cancel := context.WithCancel(t.Context())
	data := []byte(`{"action":"spawn","agent_type":"explorer","description":"inspect","prompt":"first"}`)
	returned := make(chan *tool.ContentBlock, 1)
	go func() { result, _ := task.Invoke(ctx, uctx, data); returned <- result }()
	var receipt *tool.ContentBlock
	select {
	case receipt = <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("spawn waited for task completion")
	}
	requireTask(t, receipt)
	if receipt.AgentID == "" || receipt.RunID == "" || !strings.Contains(receipt.Text, "status: submitted") || strings.Contains(receipt.Text, "finished first") {
		t.Fatalf("not a submission receipt: %+v", receipt)
	}
	cfg := <-runner.started
	if cfg.ID != agent.AgentID(receipt.AgentID) || cfg.RunID != receipt.RunID || !cfg.Detached {
		t.Fatalf("wrong durable identity: %+v", cfg)
	}
	// The tool call's context may end immediately after returning.
	cancel()
	status := invokeTaskRaw(t, task, uctx, map[string]any{"action": "status", "agent_id": receipt.AgentID})
	requireTask(t, status)
	if !strings.Contains(status.Text, `"state":"running"`) {
		t.Fatalf("tool return cancelled child: %s", status.Text)
	}
	timed := invokeTaskRaw(t, task, uctx, map[string]any{"action": "wait", "agent_id": receipt.AgentID, "run_id": receipt.RunID, "timeout_ms": 5})
	requireTask(t, timed)
	if !strings.Contains(timed.Text, `"timed_out":true`) {
		t.Fatalf("wait should time out: %s", timed.Text)
	}
	busy := invokeTaskRaw(t, task, uctx, map[string]any{"action": "resume", "agent_id": receipt.AgentID, "prompt": "too early"})
	if !busy.IsError || !strings.Contains(busy.Text, "busy") {
		t.Fatalf("busy instance was replaced: %+v", busy)
	}
	runner.release <- struct{}{}
	firstResult := waitAsyncResult(t, task, uctx, receipt)
	if !strings.Contains(firstResult.Text, "finished first") {
		t.Fatal(firstResult.Text)
	}
	resumed := invokeTaskRaw(t, task, uctx, map[string]any{"action": "resume", "agent_id": receipt.AgentID, "prompt": "second"})
	requireTask(t, resumed)
	<-runner.started
	if resumed.AgentID != receipt.AgentID || resumed.RunID == receipt.RunID {
		t.Fatal("resume identity changed or reused run_id")
	}
	// An old receipt waits for the old run, not a subsequent active resume.
	if got := waitAsyncResult(t, task, uctx, receipt); !strings.Contains(got.Text, "finished first") {
		t.Fatal(got.Text)
	}
	staleCancel := invokeTaskRaw(t, task, uctx, map[string]any{"action": "cancel", "agent_id": receipt.AgentID, "run_id": receipt.RunID})
	if !staleCancel.IsError {
		t.Fatal("old run cancellation targeted a new run")
	}
	runner.release <- struct{}{}
	if got := waitAsyncResult(t, task, uctx, resumed); !strings.Contains(got.Text, "finished second") {
		t.Fatal(got.Text)
	}
	// The new coordinator can retrieve a completed pinned run across restart.
	restarted := tool.NewTaskTool(agent.NewCoordinator(runner, agentsession.NewFileStore(storeDir)))
	if got := waitAsyncResult(t, restarted, uctx, receipt); !strings.Contains(got.Text, "finished first") {
		t.Fatal(got.Text)
	}
}

func TestTaskAsyncWaitCancellationAndExplicitCancel(t *testing.T) {
	runner := &asyncGateRunner{started: make(chan agent.AgentConfig, 1), release: make(chan struct{})}
	c := agent.NewCoordinator(runner)
	t.Cleanup(c.Close)
	task := tool.NewTaskTool(c)
	uctx := &tool.UseContext{WorkDir: t.TempDir(), SessionID: "session"}
	receipt := invokeTaskRaw(t, task, uctx, map[string]any{"action": "spawn", "description": "cancel", "prompt": "work"})
	requireTask(t, receipt)
	<-runner.started
	waitCtx, cancel := context.WithCancel(t.Context())
	cancel()
	input, _ := json.Marshal(map[string]any{"action": "wait", "agent_id": receipt.AgentID})
	result, err := task.Invoke(waitCtx, uctx, input)
	if err != nil || !result.IsError {
		t.Fatalf("cancelled wait: %+v %v", result, err)
	}
	status := invokeTaskRaw(t, task, uctx, map[string]any{"action": "status", "agent_id": receipt.AgentID})
	if !strings.Contains(status.Text, `"state":"running"`) {
		t.Fatalf("cancelled wait cancelled agent: %s", status.Text)
	}
	requireTask(t, invokeTaskRaw(t, task, uctx, map[string]any{"action": "cancel", "agent_id": receipt.AgentID, "run_id": receipt.RunID}))
	if got := waitAsyncResult(t, task, uctx, receipt); !strings.Contains(got.Text, "context canceled") {
		t.Fatal(got.Text)
	}
}

func TestTaskAsyncFailureIsVisibleAndMailboxIsSessionScoped(t *testing.T) {
	runner := &reusableRunner{failFirst: true}
	c := agent.NewCoordinator(runner)
	t.Cleanup(c.Close)
	task := tool.NewTaskTool(c)
	uctx := &tool.UseContext{WorkDir: t.TempDir(), SessionID: "parent", AgentID: "main"}
	receipt := invokeTaskRaw(t, task, uctx, map[string]any{"action": "spawn", "description": "failure", "prompt": "work"})
	requireTask(t, receipt)
	result := waitAsyncResult(t, task, uctx, receipt)
	if !strings.Contains(result.Text, "temporary model failure") || len(runner.configs) != 1 {
		t.Fatalf("submission silently retried/replaced run: %s", result.Text)
	}
	source := task.(tool.AgentNotificationSource)
	stranger := *uctx
	stranger.SessionID = "other-parent"
	if got := source.DrainAgentNotifications(&stranger); len(got) != 0 {
		t.Fatal("notification leaked to another session")
	}
	stranger = *uctx
	stranger.WorkDir = t.TempDir()
	if got := source.DrainAgentNotifications(&stranger); len(got) != 0 {
		t.Fatal("notification leaked to another project")
	}
	notifications := source.DrainAgentNotifications(uctx)
	if len(notifications) != 1 || !strings.Contains(notifications[0], receipt.RunID) || !strings.Contains(notifications[0], "temporary model failure") {
		t.Fatalf("missing failure notification: %v", notifications)
	}
	if got := source.DrainAgentNotifications(uctx); len(got) != 0 {
		t.Fatal("notification was repeated")
	}
}

func TestTaskAsyncValidationAndCoordinatorShutdown(t *testing.T) {
	runner := &asyncGateRunner{started: make(chan agent.AgentConfig, 1), release: make(chan struct{})}
	c := agent.NewCoordinator(runner)
	t.Cleanup(c.Close)
	task := tool.NewTaskTool(c)
	uctx := &tool.UseContext{WorkDir: t.TempDir()}
	for _, input := range []map[string]any{
		{"action": "spawn", "tasks": []map[string]any{{"description": "graph", "prompt": "work"}}},
		{"action": "spawn", "agent_type": "missing", "description": "bad", "prompt": "work"},
		{"action": "resume", "agent_id": "missing", "prompt": "work"},
	} {
		if result := invokeTaskRaw(t, task, uctx, input); !result.IsError {
			t.Fatalf("accepted invalid submission: %+v", result)
		}
	}
	receipt := invokeTaskRaw(t, task, uctx, map[string]any{"action": "spawn", "description": "shutdown", "prompt": "work"})
	requireTask(t, receipt)
	<-runner.started
	for _, timeout := range []int{-1, 300001} {
		if result := invokeTaskRaw(t, task, uctx, map[string]any{"action": "wait", "agent_id": receipt.AgentID, "timeout_ms": timeout}); !result.IsError {
			t.Fatal("invalid wait timeout accepted")
		}
	}
	c.Close()
	if got := waitAsyncResult(t, task, uctx, receipt); !strings.Contains(got.Text, "context canceled") {
		t.Fatal(got.Text)
	}
	if result := invokeTaskRaw(t, task, uctx, map[string]any{"action": "spawn", "description": "closed", "prompt": "work"}); !result.IsError {
		t.Fatal("closed coordinator started a child")
	}
}
