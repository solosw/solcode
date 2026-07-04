package unit_tests

import (
	"context"
	"testing"
	"time"

	"github.com/solosw/codeplus-agent/internal/agent"
)

type staticAgentRunner struct {
	result agent.AgentResult
}

func (r staticAgentRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	return r.result
}

type blockingAgentRunner struct{}

func (blockingAgentRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	<-ctx.Done()
	return agent.AgentResult{Error: ctx.Err().Error()}
}

func TestCoordinator_SpawnWaitAndListCompletedAgent(t *testing.T) {
	coordinator := agent.NewCoordinator(staticAgentRunner{
		result: agent.AgentResult{
			Output: "child finished",
		},
	})

	agentID, err := coordinator.Spawn(context.Background(), agent.AgentConfig{
		ID:       "agent-1",
		ParentID: "main",
		Role:     agent.AgentRoleSub,
		WorkDir:  "/tmp/project",
		Prompt:   "inspect files",
	})
	if err != nil {
		t.Fatalf("spawn agent: %v", err)
	}
	if agentID != "agent-1" {
		t.Fatalf("expected agent-1, got %q", agentID)
	}

	result, err := coordinator.Wait(context.Background(), agentID)
	if err != nil {
		t.Fatalf("wait agent: %v", err)
	}
	if result.AgentID != agentID {
		t.Fatalf("expected result agent id %q, got %q", agentID, result.AgentID)
	}
	if result.Output != "child finished" {
		t.Fatalf("expected child output, got %q", result.Output)
	}

	statuses := coordinator.List()
	if len(statuses) != 1 {
		t.Fatalf("expected one agent status, got %d", len(statuses))
	}
	if statuses[0].ID != agentID {
		t.Fatalf("expected status id %q, got %q", agentID, statuses[0].ID)
	}
	if statuses[0].ParentID != "main" {
		t.Fatalf("expected parent main, got %q", statuses[0].ParentID)
	}
	if statuses[0].State != agent.AgentCompleted {
		t.Fatalf("expected completed state, got %q", statuses[0].State)
	}
}

func TestCoordinator_EmitsStartedAndCompletedEvents(t *testing.T) {
	coordinator := agent.NewCoordinator(staticAgentRunner{
		result: agent.AgentResult{Output: "done"},
	})
	events := make(chan agent.Event, 2)
	coordinator.SetEventHandler(func(event agent.Event) {
		events <- event
	})

	agentID, err := coordinator.Spawn(context.Background(), agent.AgentConfig{
		ID:          "agent-events",
		ParentID:    "main",
		Role:        agent.AgentRoleTask,
		Description: "Review files",
		Prompt:      "inspect",
	})
	if err != nil {
		t.Fatalf("spawn agent: %v", err)
	}
	if _, err := coordinator.Wait(context.Background(), agentID); err != nil {
		t.Fatalf("wait agent: %v", err)
	}

	started := <-events
	completed := <-events
	if started.Kind != agent.EventStarted || started.Status.State != agent.AgentRunning {
		t.Fatalf("unexpected started event: %#v", started)
	}
	if started.Description != "Review files" {
		t.Fatalf("expected description propagated, got %q", started.Description)
	}
	if completed.Kind != agent.EventCompleted || completed.Status.State != agent.AgentCompleted {
		t.Fatalf("unexpected completed event: %#v", completed)
	}
	if completed.Result.Output != "done" {
		t.Fatalf("expected completed output, got %q", completed.Result.Output)
	}
}

func TestCoordinator_CancelStopsRunningAgent(t *testing.T) {
	coordinator := agent.NewCoordinator(blockingAgentRunner{})

	agentID, err := coordinator.Spawn(context.Background(), agent.AgentConfig{
		ID:     "agent-cancel",
		Role:   agent.AgentRoleSub,
		Prompt: "long task",
	})
	if err != nil {
		t.Fatalf("spawn agent: %v", err)
	}

	if err := coordinator.Cancel(agentID); err != nil {
		t.Fatalf("cancel agent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := coordinator.Wait(ctx, agentID)
	if err != nil {
		t.Fatalf("wait cancelled agent: %v", err)
	}
	if result.AgentID != agentID {
		t.Fatalf("expected result agent id %q, got %q", agentID, result.AgentID)
	}

	statuses := coordinator.List()
	if len(statuses) != 1 {
		t.Fatalf("expected one status, got %d", len(statuses))
	}
	if statuses[0].State != agent.AgentCancelled {
		t.Fatalf("expected cancelled state, got %q", statuses[0].State)
	}
}

func TestCoordinator_RejectsDuplicateAgentID(t *testing.T) {
	coordinator := agent.NewCoordinator(staticAgentRunner{
		result: agent.AgentResult{Output: "done"},
	})

	_, err := coordinator.Spawn(context.Background(), agent.AgentConfig{
		ID:     "same-agent",
		Role:   agent.AgentRoleSub,
		Prompt: "first task",
	})
	if err != nil {
		t.Fatalf("first spawn should succeed: %v", err)
	}

	_, err = coordinator.Spawn(context.Background(), agent.AgentConfig{
		ID:     "same-agent",
		Role:   agent.AgentRoleSub,
		Prompt: "second task",
	})
	if err == nil {
		t.Fatalf("expected duplicate agent id to be rejected")
	}
}

func TestCoordinator_ChildrenReturnsAgentsForParent(t *testing.T) {
	coordinator := agent.NewCoordinator(blockingAgentRunner{})

	_, err := coordinator.Spawn(context.Background(), agent.AgentConfig{
		ID:       "child-1",
		ParentID: "main",
		Role:     agent.AgentRoleTask,
		Prompt:   "first child",
	})
	if err != nil {
		t.Fatalf("spawn child-1: %v", err)
	}
	_, err = coordinator.Spawn(context.Background(), agent.AgentConfig{
		ID:       "child-2",
		ParentID: "main",
		Role:     agent.AgentRoleTask,
		Prompt:   "second child",
	})
	if err != nil {
		t.Fatalf("spawn child-2: %v", err)
	}
	_, err = coordinator.Spawn(context.Background(), agent.AgentConfig{
		ID:       "other-child",
		ParentID: "other-parent",
		Role:     agent.AgentRoleTask,
		Prompt:   "other child",
	})
	if err != nil {
		t.Fatalf("spawn other-child: %v", err)
	}
	defer coordinator.Cancel("child-1")
	defer coordinator.Cancel("child-2")
	defer coordinator.Cancel("other-child")

	children := coordinator.Children("main")
	if len(children) != 2 {
		t.Fatalf("expected two children for main, got %d", len(children))
	}
	seen := map[agent.AgentID]bool{}
	for _, child := range children {
		seen[child.ID] = true
		if child.ParentID != "main" {
			t.Fatalf("expected child parent main, got %q", child.ParentID)
		}
	}
	if !seen["child-1"] || !seen["child-2"] {
		t.Fatalf("expected child-1 and child-2, got %#v", seen)
	}
}
