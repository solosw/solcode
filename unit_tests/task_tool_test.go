package unit_tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
	cfg agent.AgentConfig
}

func (r *capturingAgentRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	r.cfg = cfg
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
	if runner.cfg.Description != "Summarize package" {
		t.Fatalf("expected description propagated, got %q", runner.cfg.Description)
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
