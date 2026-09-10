package unit_tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/tool"
)

func TestSubagentTool_SpawnsAndReturnsResult(t *testing.T) {
	coordinator := agent.NewCoordinator(staticAgentRunner{
		result: agent.AgentResult{Output: "sub-agent summary"},
	})
	sub := tool.NewSubagentTool(coordinator)

	result, err := sub.Invoke(context.Background(), &tool.UseContext{
		SessionID: "session-1",
		MessageID: "toolu_parent",
		AgentID:   "main-agent",
		WorkDir:   "/tmp/project",
	}, json.RawMessage(`{
		"description":"Review files",
		"prompt":"Inspect the tool package",
		"allowed_tools":["View","Grep"],
		"task_id":"task_1"
	}`))
	if err != nil {
		t.Fatalf("invoke subagent: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got %s", result.Text)
	}
	if result.Text != "sub-agent summary" {
		t.Fatalf("result = %q", result.Text)
	}

	statuses := coordinator.List()
	if len(statuses) != 1 {
		t.Fatalf("expected one spawned agent, got %d", len(statuses))
	}
	if statuses[0].ParentID != "main-agent" {
		t.Fatalf("parent = %q", statuses[0].ParentID)
	}
	if statuses[0].Role != agent.AgentRoleTask {
		t.Fatalf("role = %q", statuses[0].Role)
	}
}

func TestSubagentTool_RetriesAndReportsStatus(t *testing.T) {
	runner := &failThenSucceedRunner{failTimes: 1}
	coordinator := agent.NewCoordinator(runner)
	sub := tool.NewSubagentTool(coordinator)
	var statuses []string

	result, err := sub.Invoke(context.Background(), &tool.UseContext{
		AgentID:        "main",
		MessageID:      "toolu_task",
		WorkDir:        "/tmp/project",
		TaskRetryDelay: time.Millisecond,
		Status: func(status string) {
			statuses = append(statuses, status)
		},
	}, json.RawMessage(`{"description":"Flaky","prompt":"try again","task_id":"a"}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected eventual success, got %s", result.Text)
	}
	if result.Text != "recovered" {
		t.Fatalf("result = %q", result.Text)
	}
	hasRetry := false
	hasParent := false
	for _, status := range statuses {
		if strings.Contains(status, "retry 1/5") {
			hasRetry = true
		}
		if strings.Contains(status, "parent_tool_use_id=toolu_task") {
			hasParent = true
		}
	}
	if !hasRetry {
		t.Fatalf("expected retry status, got %v", statuses)
	}
	if !hasParent {
		t.Fatalf("expected parent tool use id in status, got %v", statuses)
	}
}

func TestSubagentTool_UsesFastModelForEasy(t *testing.T) {
	runner := &capturingAgentRunner{}
	coordinator := agent.NewCoordinator(runner)
	sub := tool.NewSubagentTool(coordinator)

	_, err := sub.Invoke(context.Background(), &tool.UseContext{
		AgentID:   "main",
		WorkDir:   "/tmp/project",
		FastModel: "fast-model",
	}, json.RawMessage(`{"description":"Easy","prompt":"do it","difficulty":"easy"}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(runner.cfgs) != 1 || runner.cfgs[0].Model != "fast-model" {
		t.Fatalf("expected fast model, got %#v", runner.cfgs)
	}
}

func TestSubagentTool_IsHiddenFromModelFlags(t *testing.T) {
	sub := tool.NewSubagentTool(agent.NewCoordinator(staticAgentRunner{}))
	if sub.Name() != tool.SubagentToolName {
		t.Fatalf("name = %q", sub.Name())
	}
	if sub.IsReadOnly(nil) {
		t.Fatal("Subagent must not be read-only")
	}
}

type failThenSucceedRunner struct {
	failTimes int
	calls     int
}

func (r *failThenSucceedRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	r.calls++
	if r.calls <= r.failTimes {
		return agent.AgentResult{Error: "temporary failure"}
	}
	return agent.AgentResult{Output: "recovered"}
}
