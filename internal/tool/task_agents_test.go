package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
	"github.com/solosw/solcode/internal/tool"
)

type reusableRunner struct {
	mu        sync.Mutex
	configs   []agent.AgentConfig
	failFirst bool
}

func (r *reusableRunner) Run(_ context.Context, cfg agent.AgentConfig) agent.AgentResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configs = append(r.configs, cfg)
	if r.failFirst && len(r.configs) == 1 {
		return agent.AgentResult{Error: "temporary model failure"}
	}
	return agent.AgentResult{Output: "bounded result"}
}

// Existing lifecycle/role assertions inspect completed histories; explicitly wait
// in this test helper. Async scheduling tests use invokeTaskRaw instead.
func invokeTask(t *testing.T, task tool.Tool, uctx *tool.UseContext, input any) *tool.ContentBlock {
	t.Helper()
	result := invokeTaskRaw(t, task, uctx, input)
	if result != nil && !result.IsError && strings.Contains(result.Text, "status: submitted") {
		waited := invokeTaskRaw(t, task, uctx, map[string]any{"action": "wait", "agent_id": result.AgentID, "run_id": result.RunID})
		requireTask(t, waited)
	}
	return result
}

func invokeTaskRaw(t *testing.T, task tool.Tool, uctx *tool.UseContext, input any) *tool.ContentBlock {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := task.Invoke(t.Context(), uctx, data)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func requireTask(t *testing.T, result *tool.ContentBlock) {
	t.Helper()
	if result == nil || result.IsError {
		t.Fatalf("Task failed: %+v", result)
	}
}
func resultAgentID(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "agent_id: ") {
			return strings.TrimPrefix(line, "agent_id: ")
		}
	}
	return ""
}

func TestTaskReuseManagementAndCrossRestartHistory(t *testing.T) {
	dir := t.TempDir()
	runner := &reusableRunner{}
	c := agent.NewCoordinator(runner, agentsession.NewFileStore(dir))
	task := tool.NewTaskTool(c)
	uctx := &tool.UseContext{AgentID: "main", SessionID: "session", WorkDir: t.TempDir(), TaskRetryDelay: time.Millisecond}
	result := invokeTask(t, task, uctx, map[string]any{"description": "first", "prompt": "inspect"})
	requireTask(t, result)
	id := resultAgentID(result.Text)
	if id == "" {
		t.Fatalf("agent id missing: %s", result.Text)
	}
	task = tool.NewTaskTool(agent.NewCoordinator(runner, agentsession.NewFileStore(dir)))
	result = invokeTask(t, task, uctx, map[string]any{"description": "second", "prompt": "continue", "agent_id": id})
	requireTask(t, result)
	if resultAgentID(result.Text) != id {
		t.Fatal("resume spawned another child")
	}
	list := invokeTask(t, task, uctx, map[string]any{"action": "list"})
	requireTask(t, list)
	if !strings.Contains(list.Text, id) {
		t.Fatal("list lost child")
	}
	history := invokeTask(t, task, uctx, map[string]any{"action": "history", "agent_id": id, "offset": 0, "limit": 1})
	requireTask(t, history)
	var page struct {
		Messages []json.RawMessage `json:"messages"`
		Total    int               `json:"total_messages"`
		Next     int               `json:"next_offset"`
	}
	if err := json.Unmarshal([]byte(history.Text), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Total != 4 || page.Next != 1 {
		t.Fatalf("history: %s", history.Text)
	}
	stranger := *uctx
	stranger.SessionID = "another-session"
	stranger.AgentID = "another-main"
	requireTask(t, invokeTask(t, task, &stranger, map[string]any{"action": "history", "agent_id": id}))
	stranger.WorkDir = t.TempDir()
	if result := invokeTask(t, task, &stranger, map[string]any{"action": "history", "agent_id": id}); !result.IsError {
		t.Fatal("foreign project history allowed")
	}
	archive := invokeTask(t, task, uctx, map[string]any{"action": "archive", "agent_id": id})
	requireTask(t, archive)
	if result := invokeTask(t, task, uctx, map[string]any{"description": "archived", "prompt": "resume", "agent_id": id}); !result.IsError || !strings.Contains(result.Text, "archived") {
		t.Fatalf("archive: %+v", result)
	}
	if result := invokeTask(t, task, uctx, map[string]any{"description": "unknown", "prompt": "resume", "agent_id": "missing"}); !result.IsError {
		t.Fatal("unknown id silently created")
	}
}

func TestTaskProjectActionsAcrossSessions(t *testing.T) {
	dir, workDir := t.TempDir(), t.TempDir()
	runner := &reusableRunner{}
	task := tool.NewTaskTool(agent.NewCoordinator(runner, agentsession.NewFileStore(dir)))
	first := &tool.UseContext{AgentID: "main-one", SessionID: "session-one", WorkDir: workDir}
	created := invokeTask(t, task, first, map[string]any{"action": "spawn", "description": "inspect", "prompt": "first task", "allowed_tools": []string{"View"}})
	requireTask(t, created)
	id := resultAgentID(created.Text)
	if id == "" {
		t.Fatal("spawn did not return an agent id")
	}
	// Another project can share the physical store without leaking into list.
	other := &tool.UseContext{AgentID: "other-main", SessionID: "other-session", WorkDir: t.TempDir()}
	foreign := invokeTask(t, task, other, map[string]any{"action": "spawn", "description": "other", "prompt": "other project"})
	requireTask(t, foreign)
	foreignID := resultAgentID(foreign.Text)
	task = tool.NewTaskTool(agent.NewCoordinator(runner, agentsession.NewFileStore(dir)))
	second := &tool.UseContext{AgentID: "main-two", SessionID: "session-two", WorkDir: workDir}
	list := invokeTask(t, task, second, map[string]any{"action": "list"})
	requireTask(t, list)
	if !strings.Contains(list.Text, id) || strings.Contains(list.Text, foreignID) {
		t.Fatalf("project list: %s", list.Text)
	}
	resumed := invokeTask(t, task, second, map[string]any{"action": "resume", "agent_id": id, "description": "continue", "prompt": "second task", "allowed_tools": []string{"View", "Write"}})
	requireTask(t, resumed)
	if resultAgentID(resumed.Text) != id {
		t.Fatal("resume created a replacement child")
	}
	if len(runner.configs) != 3 || len(runner.configs[2].AllowedTools) != 1 || runner.configs[2].AllowedTools[0] != "View" {
		t.Fatalf("resume broadened tools or launched extra models: %+v", runner.configs)
	}
	for _, action := range []string{"status", "history", "wait", "cancel", "archive"} {
		requireTask(t, invokeTask(t, task, first, map[string]any{"action": action, "agent_id": id}))
	}
	if len(runner.configs) != 3 {
		t.Fatal("management actions invoked a model")
	}
	for _, action := range []string{"status", "history", "wait", "cancel", "archive", "resume"} {
		result := invokeTask(t, task, other, map[string]any{"action": action, "agent_id": id, "description": "foreign", "prompt": "must not run"})
		if !result.IsError || !strings.Contains(result.Text, "project/workdir") {
			t.Fatalf("foreign %s accepted: %+v", action, result)
		}
	}
	if len(runner.configs) != 3 {
		t.Fatal("foreign project launched a model")
	}
}

type managedProjectRunner struct {
	started chan struct{}
	stop    chan struct{}
}

func (r *managedProjectRunner) Run(ctx context.Context, _ agent.AgentConfig) agent.AgentResult {
	close(r.started)
	<-ctx.Done()
	<-r.stop // Cancellation must not release ownership until the runner exits.
	return agent.AgentResult{Error: ctx.Err().Error(), NoRetry: true}
}

func TestTaskProjectRunningManagement(t *testing.T) {
	workDir := t.TempDir()
	runner := &managedProjectRunner{started: make(chan struct{}), stop: make(chan struct{})}
	release := sync.OnceFunc(func() { close(runner.stop) })
	c := agent.NewCoordinator(runner, agentsession.NewFileStore(t.TempDir()))
	defer release()
	defer c.Cancel("running-child")
	cfg := agent.AgentConfig{ID: "running-child", ParentID: "first-main", ParentSessionID: "first-session", WorkDir: workDir, Prompt: "work"}
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	task := tool.NewTaskTool(c)
	second := &tool.UseContext{AgentID: "second-main", SessionID: "second-session", WorkDir: workDir}
	status := invokeTask(t, task, second, map[string]any{"action": "status", "agent_id": cfg.ID})
	requireTask(t, status)
	if !strings.Contains(status.Text, `"state":"running"`) {
		t.Fatalf("not running: %s", status.Text)
	}
	requireTask(t, invokeTask(t, task, second, map[string]any{"action": "cancel", "agent_id": cfg.ID}))
	for _, action := range []string{"resume", "archive"} {
		result := invokeTask(t, task, second, map[string]any{"action": action, "agent_id": cfg.ID, "description": "busy", "prompt": "must not start"})
		if !result.IsError || !strings.Contains(result.Text, "busy") {
			t.Fatalf("cancelled worker ownership lost: %+v", result)
		}
	}
	release()
	requireTask(t, invokeTask(t, task, second, map[string]any{"action": "wait", "agent_id": cfg.ID}))
	requireTask(t, invokeTask(t, task, second, map[string]any{"action": "archive", "agent_id": cfg.ID}))
}

func TestTaskExplicitActionValidation(t *testing.T) {
	runner := &reusableRunner{}
	task := tool.NewTaskTool(agent.NewCoordinator(runner))
	uctx := &tool.UseContext{AgentID: "main", SessionID: "session", WorkDir: t.TempDir()}
	for _, input := range []map[string]any{
		{"action": "spawn", "agent_id": "existing", "description": "bad", "prompt": "bad"},
		{"action": "resume", "description": "bad", "prompt": "bad"},
		{"action": "resume", "agent_id": "  ", "description": "bad", "prompt": "bad"},
		{"action": "invalid"},
		{"action": "spawn", "tasks": []map[string]any{{"description": "valid", "prompt": "first"}, {"agent_id": "existing", "description": "invalid", "prompt": "second"}}},
		{"action": "resume", "tasks": []map[string]any{{"agent_id": "existing", "description": "first", "prompt": "first"}, {"description": "missing id", "prompt": "second"}}},
	} {
		if result := invokeTask(t, task, uctx, input); !result.IsError {
			t.Fatalf("invalid action accepted: %+v", input)
		}
	}
	if len(runner.configs) != 0 {
		t.Fatal("validation ran a model before rejecting the request")
	}
	// Legacy action=run and omitted action retain automatic spawn/resume.
	created := invokeTask(t, task, uctx, map[string]any{"action": "run", "description": "legacy", "prompt": "first"})
	requireTask(t, created)
	resumed := invokeTask(t, task, uctx, map[string]any{"agent_id": resultAgentID(created.Text), "description": "legacy", "prompt": "second"})
	requireTask(t, resumed)
	if resultAgentID(resumed.Text) != resultAgentID(created.Text) || len(runner.configs) != 2 {
		t.Fatal("legacy run compatibility lost")
	}
}

func TestTaskRetryKeepsOneAgentAndRecordsAttempts(t *testing.T) {
	runner := &reusableRunner{failFirst: true}
	c := agent.NewCoordinator(runner)
	task := tool.NewTaskTool(c)
	var events []tool.AgentProgressEvent
	uctx := &tool.UseContext{AgentID: "main", SessionID: "session", TaskRetryDelay: time.Millisecond, OnAgentProgress: func(event tool.AgentProgressEvent) { events = append(events, event) }}
	result := invokeTask(t, task, uctx, map[string]any{"description": "retry", "prompt": "inspect"})
	requireTask(t, result)
	if len(c.List()) != 1 || len(runner.configs) != 2 {
		t.Fatal("retries created extra agents")
	}
	one, two := runner.configs[0], runner.configs[1]
	if one.ID != two.ID || one.RunID == two.RunID || one.ExecutionID != two.ExecutionID || one.Attempt != 1 || two.Attempt != 2 {
		t.Fatalf("attempt correlation: %+v %+v", one, two)
	}
	if len(events) != 5 {
		t.Fatalf("progress events: %+v", events)
	}
	for i, event := range events {
		wantRun := one.RunID
		if i >= 3 {
			wantRun = two.RunID
		}
		if event.AgentID != string(one.ID) || event.RunID != wantRun {
			t.Fatalf("progress lost run correlation: %+v", events)
		}
	}
}

func TestTaskDependencyOnSameAgentDoesNotDuplicatePriorResult(t *testing.T) {
	runner := &reusableRunner{}
	c := agent.NewCoordinator(runner)
	task := tool.NewTaskTool(c)
	uctx := &tool.UseContext{AgentID: "main", SessionID: "session"}
	initial := invokeTask(t, task, uctx, map[string]any{"description": "initial", "prompt": "inspect"})
	requireTask(t, initial)
	id := resultAgentID(initial.Text)
	result := invokeTask(t, task, uctx, map[string]any{"tasks": []map[string]any{{"id": "one", "description": "one", "prompt": "first step", "agent_id": id}, {"id": "two", "description": "two", "prompt": "second step", "agent_id": id, "depends_on": []string{"one"}}}})
	requireTask(t, result)
	if len(runner.configs) != 3 || runner.configs[2].Prompt != "second step" {
		t.Fatalf("previous results duplicated: %+v", runner.configs)
	}
	result = invokeTask(t, task, uctx, map[string]any{"tasks": []map[string]any{{"id": "one", "description": "one", "prompt": "first step", "agent_id": id}, {"id": "two", "description": "two", "prompt": "second step", "agent_id": id}}})
	if !result.IsError || len(runner.configs) != 3 {
		t.Fatalf("concurrent duplicate assignments not rejected before execution: %+v", result)
	}
}

func TestTaskRejectsNestedAndKeepsCoreSchema(t *testing.T) {
	task := tool.NewTaskTool(agent.NewCoordinator(&reusableRunner{}))
	result := invokeTask(t, task, &tool.UseContext{AgentRole: "task"}, map[string]any{"action": "list"})
	if !result.IsError {
		t.Fatal("nested Task accepted")
	}
	properties := task.InputSchema()["properties"].(map[string]any)
	for _, name := range []string{"agent_id", "fork_context", "action", "tasks", "run_in_background"} {
		if properties[name] == nil {
			t.Fatalf("missing property %s", name)
		}
	}
}
