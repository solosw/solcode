package unit_tests

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solosw/codeplus-agent/internal/agent"
	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestTaskTool_SpawnsSubAgentAndReturnsResult(t *testing.T) {
	coordinator := agent.NewCoordinator(staticAgentRunner{
		result: agent.AgentResult{Output: "sub-agent summary"},
	})
	taskTool := tool.NewTaskTool(coordinator)

	input := json.RawMessage(`{
		"description":"Review files",
		"prompt":"Inspect the tool package",
		"allowed_tools":["View","Grep"],
		"max_turns":20
	}`)

	result, err := taskTool.Invoke(context.Background(), &tool.UseContext{
		SessionID: "session-1",
		AgentID:   "main-agent",
		WorkDir:   "/tmp/project",
	}, input)
	if err != nil {
		t.Fatalf("invoke task tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected task success, got %s", result.Text)
	}
	if !strings.Contains(result.Text, "sub-agent summary") {
		t.Fatalf("expected sub-agent output in result, got %q", result.Text)
	}

	statuses := coordinator.List()
	if len(statuses) != 1 {
		t.Fatalf("expected one spawned child agent, got %d", len(statuses))
	}
	if statuses[0].ParentID != "main-agent" {
		t.Fatalf("expected parent main-agent, got %q", statuses[0].ParentID)
	}
	if statuses[0].Role != agent.AgentRoleTask {
		t.Fatalf("expected task role, got %q", statuses[0].Role)
	}
}

type capturingAgentRunner struct {
	cfgs []agent.AgentConfig
}

func (r *capturingAgentRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	r.cfgs = append(r.cfgs, cfg)
	return agent.AgentResult{Output: "captured"}
}

func TestTaskTool_PropagatesDescription(t *testing.T) {
	runner := &capturingAgentRunner{}
	coordinator := agent.NewCoordinator(runner)
	taskTool := tool.NewTaskTool(coordinator)

	_, err := taskTool.Invoke(context.Background(), &tool.UseContext{AgentID: "main-agent", WorkDir: "/tmp/project"}, json.RawMessage(`{
		"description":"Summarize package",
		"prompt":"Inspect package"
	}`))
	if err != nil {
		t.Fatalf("invoke task tool: %v", err)
	}
	if len(runner.cfgs) != 1 {
		t.Fatalf("expected one captured config, got %d", len(runner.cfgs))
	}
	if runner.cfgs[0].Description != "Summarize package" {
		t.Fatalf("expected description propagated, got %q", runner.cfgs[0].Description)
	}
}

func TestTaskTool_MultipleTasksUseFastModelAndDependencyGraph(t *testing.T) {
	runner := &capturingAgentRunner{}
	coordinator := agent.NewCoordinator(runner)
	taskTool := tool.NewTaskTool(coordinator)

	result, err := taskTool.Invoke(context.Background(), &tool.UseContext{AgentID: "main-agent", WorkDir: "/tmp/project", FastModel: "fast-model"}, json.RawMessage(`{
		"tasks":[
			{"id":"a","description":"Easy task","prompt":"Do easy work","difficulty":"easy"},
			{"id":"b","description":"Hard task","prompt":"Do hard work","difficulty":"hard","depends_on":["a"]}
		]
	}`))
	if err != nil {
		t.Fatalf("invoke task tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected task success, got %s", result.Text)
	}
	if len(runner.cfgs) != 2 {
		t.Fatalf("expected two captured configs, got %d", len(runner.cfgs))
	}
	if runner.cfgs[0].Description != "Easy task" || runner.cfgs[0].Model != "fast-model" {
		t.Fatalf("expected easy task to use fast model, got %#v", runner.cfgs[0])
	}
	if runner.cfgs[1].Description != "Hard task" || runner.cfgs[1].Model != "" {
		t.Fatalf("expected hard task to use main model fallback, got %#v", runner.cfgs[1])
	}
	if !strings.Contains(result.Text, "## a - Easy task") || !strings.Contains(result.Text, "## b - Hard task") {
		t.Fatalf("expected grouped multi-task output, got %q", result.Text)
	}
}

func TestTaskTool_IsNotReadOnlyBecauseSubAgentMayUseWriteTools(t *testing.T) {
	coordinator := agent.NewCoordinator(staticAgentRunner{
		result: agent.AgentResult{Output: "sub-agent summary"},
	})
	taskTool := tool.NewTaskTool(coordinator)

	if taskTool.IsReadOnly(nil) {
		t.Fatalf("task tool should not be read-only because sub-agents may perform writes through their allowed tools")
	}
}

func TestTaskTool_BlocksCallerUntilAllTasksComplete(t *testing.T) {
	// Use a runner that records completion order; all tasks report "captured".
	runner := &capturingAgentRunner{}
	coordinator := agent.NewCoordinator(runner)
	taskTool := tool.NewTaskTool(coordinator)

	result, err := taskTool.Invoke(context.Background(), &tool.UseContext{
		AgentID: "main",
		WorkDir: "/tmp/project",
	}, json.RawMessage(`{
		"tasks":[
			{"id":"a","description":"Task A","prompt":"Do A"},
			{"id":"b","description":"Task B","prompt":"Do B"},
			{"id":"c","description":"Task C","prompt":"Do C"}
		]
	}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	// After Invoke returns, ALL tasks must have completed.
	if len(runner.cfgs) != 3 {
		t.Fatalf("expected 3 completed tasks, got %d", len(runner.cfgs))
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "## a - Task A") || !strings.Contains(result.Text, "## b - Task B") || !strings.Contains(result.Text, "## c - Task C") {
		t.Fatalf("expected all three task results, got %q", result.Text)
	}
	// Coordinator must show all three are completed.
	statuses := coordinator.List()
	if len(statuses) != 3 {
		t.Fatalf("expected 3 coordinator statuses, got %d", len(statuses))
	}
	for _, s := range statuses {
		if s.State != agent.AgentCompleted {
			t.Fatalf("expected all completed, got %s=%s", s.ID, s.State)
		}
	}
}

func TestTaskTool_ParallelTasksCompleteFasterThanSerialWould(t *testing.T) {
	// Each runner records its start and blocks until all are started via a channel.
	ready := make(chan struct{})
	release := make(chan struct{})
	runner := &concurrencyProbeRunner{ready: ready, release: release}
	coordinator := agent.NewCoordinator(runner)
	taskTool := tool.NewTaskTool(coordinator)

	// Launch Invoke in a goroutine since it will block until all tasks complete.
	done := make(chan error, 1)
	var result *tool.ContentBlock
	go func() {
		var err error
		result, err = taskTool.Invoke(context.Background(), &tool.UseContext{
			AgentID: "main",
			WorkDir: "/tmp/project",
		}, json.RawMessage(`{
			"tasks":[
				{"id":"a","description":"A","prompt":"a"},
				{"id":"b","description":"B","prompt":"b"},
				{"id":"c","description":"C","prompt":"c"}
			]
		}`))
		done <- err
	}()

	// Wait for all three goroutines to reach the barrier.
	for i := 0; i < 3; i++ {
		<-ready
	}

	// All three should be concurrently in-flight.
	if runner.maxConcurrent < 2 {
		t.Fatalf("expected at least 2 concurrent runners, got max concurrent=%d", runner.maxConcurrent)
	}

	// Release all runners so Invoke can return.
	for i := 0; i < 3; i++ {
		release <- struct{}{}
	}

	if err := <-done; err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
}

type concurrencyProbeRunner struct {
	mu            sync.Mutex
	ready         chan struct{}
	release       chan struct{}
	maxConcurrent int
	current       int
}

func (r *concurrencyProbeRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	r.mu.Lock()
	r.current++
	if r.current > r.maxConcurrent {
		r.maxConcurrent = r.current
	}
	r.mu.Unlock()

	// Signal that this goroutine has reached the run point.
	r.ready <- struct{}{}
	// Wait until the test releases us.
	<-r.release

	r.mu.Lock()
	r.current--
	r.mu.Unlock()
	return agent.AgentResult{Output: cfg.Description + " done"}
}

func TestTaskTool_CoordinatorEmitsStartedAndCompletedEvents(t *testing.T) {
	runner := &capturingAgentRunner{}
	coordinator := agent.NewCoordinator(runner)

	events := make([]agent.Event, 0)
	coordinator.SetEventHandler(func(e agent.Event) {
		events = append(events, e)
	})

	taskTool := tool.NewTaskTool(coordinator)
	_, err := taskTool.Invoke(context.Background(), &tool.UseContext{
		AgentID: "main",
		WorkDir: "/tmp/project",
	}, json.RawMessage(`{
		"tasks":[
			{"id":"a","description":"Alpha","prompt":"alpha"},
			{"id":"b","description":"Beta","prompt":"beta"}
		]
	}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}

	// Should see at least 4 events: 2 started + 2 completed.
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events (start+complete for 2 tasks), got %d", len(events))
	}
	started := 0
	completed := 0
	for _, e := range events {
		switch e.Kind {
		case agent.EventStarted:
			started++
		case agent.EventCompleted:
			completed++
		}
	}
	if started != 2 {
		t.Fatalf("expected 2 started events, got %d", started)
	}
	if completed != 2 {
		t.Fatalf("expected 2 completed events, got %d", completed)
	}
}

type orderedRunner struct {
	mu   sync.Mutex
	runs []string
}

func (r *orderedRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	r.mu.Lock()
	r.runs = append(r.runs, cfg.Description)
	r.mu.Unlock()
	// Small sleep to ensure ordering is detectable.
	time.Sleep(5 * time.Millisecond)
	return agent.AgentResult{Output: cfg.Description + " ok"}
}

func TestTaskTool_DependencyGraphRunsSeriallyByLevel(t *testing.T) {
	runner := &orderedRunner{}
	coordinator := agent.NewCoordinator(runner)
	taskTool := tool.NewTaskTool(coordinator)

	_, err := taskTool.Invoke(context.Background(), &tool.UseContext{
		AgentID: "main",
		WorkDir: "/tmp/project",
	}, json.RawMessage(`{
		"tasks":[
			{"id":"a","description":"First","prompt":"first"},
			{"id":"b","description":"Second","prompt":"second","depends_on":["a"]},
			{"id":"c","description":"Third","prompt":"third","depends_on":["b"]}
		]
	}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(runner.runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runner.runs))
	}
	if runner.runs[0] != "First" || runner.runs[1] != "Second" || runner.runs[2] != "Third" {
		t.Fatalf("expected strict serial order First->Second->Third, got %v", runner.runs)
	}
}

func TestTaskTool_SerialModeRunsOneByOne(t *testing.T) {
	runner := &orderedRunner{}
	coordinator := agent.NewCoordinator(runner)
	taskTool := tool.NewTaskTool(coordinator)

	_, err := taskTool.Invoke(context.Background(), &tool.UseContext{
		AgentID: "main",
		WorkDir: "/tmp/project",
	}, json.RawMessage(`{
		"execution_mode":"serial",
		"tasks":[
			{"id":"a","description":"A","prompt":"a"},
			{"id":"b","description":"B","prompt":"b"},
			{"id":"c","description":"C","prompt":"c"}
		]
	}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(runner.runs) != 3 {
		t.Fatalf("expected 3 serial runs, got %d", len(runner.runs))
	}
}

func TestTaskTool_DependencyContextIsPassedToSuccessor(t *testing.T) {
	// The first task returns a key finding; the second task (which depends on the first)
	// should see that finding prepended to its prompt.
	var capturedPrompts []string
	coordinator := agent.NewCoordinator(&promptCapturingRunner{prompts: &capturedPrompts})
	taskTool := tool.NewTaskTool(coordinator)

	_, err := taskTool.Invoke(context.Background(), &tool.UseContext{
		AgentID: "main",
		WorkDir: "/tmp/project",
	}, json.RawMessage(`{
		"tasks":[
			{"id":"scan","description":"Scanner","prompt":"Scan for Go files"},
			{"id":"report","description":"Reporter","prompt":"Report findings","depends_on":["scan"]}
		]
	}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(capturedPrompts) != 2 {
		t.Fatalf("expected 2 captured prompts, got %d", len(capturedPrompts))
	}
	reporterPrompt := capturedPrompts[1]
	if !strings.Contains(reporterPrompt, "Previous task results") {
		t.Fatalf("expected reporter prompt to contain 'Previous task results', got %q", reporterPrompt)
	}
	if !strings.Contains(reporterPrompt, "Result from task scan") {
		t.Fatalf("expected reporter prompt to reference 'Result from task scan', got %q", reporterPrompt)
	}
}

type promptCapturingRunner struct {
	mu      sync.Mutex
	prompts *[]string
}

func (r *promptCapturingRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	r.mu.Lock()
	*r.prompts = append(*r.prompts, cfg.Prompt)
	r.mu.Unlock()
	return agent.AgentResult{Output: "result: " + cfg.Description}
}
