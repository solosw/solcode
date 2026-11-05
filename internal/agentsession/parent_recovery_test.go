package agentsession_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
)

func TestParentRequestSurvivesRestartAndRestoresOnlyKnownAnswer(t *testing.T) {
	dir := t.TempDir()
	store := agentsession.NewFileStore(dir)
	cfg := config()
	cfg.RunID = "old-run"
	call := sdk.NewAssistantMessage(sdk.NewToolUseBlock("ask-before-crash", map[string]any{"question": "which interface?"}, "AskParent"))
	record := &agent.Record{Config: cfg, Status: agent.AgentStatus{ID: cfg.ID, RunID: cfg.RunID, ParentID: cfg.ParentID, ParentSessionID: cfg.ParentSessionID, WorkDir: cfg.WorkDir, State: agent.AgentWaitingParent}, Messages: []sdk.MessageParam{call}, Context: []int{0}, Runs: []agent.RunRecord{{ID: cfg.RunID, State: agent.AgentWaitingParent}}, ParentRequests: []agent.ParentRequest{{ID: "request-before-crash", AgentID: cfg.ID, RunID: cfg.RunID, ToolUseID: "ask-before-crash", Question: "which interface?", CreatedAt: time.Now()}}}
	if err := store.Save(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	runner := &historyRunner{}
	coordinator := agent.NewCoordinator(runner, agentsession.NewFileStore(dir))
	defer coordinator.Close()
	requests, err := coordinator.DrainParentQuestions(t.Context(), scope(cfg))
	if err != nil || len(requests) != 1 {
		t.Fatalf("mailbox=%+v err=%v", requests, err)
	}
	viewed, err := coordinator.Get(t.Context(), cfg.ID, scope(cfg))
	if err != nil || viewed.Status.State != agent.AgentInterrupted {
		t.Fatalf("stale waiting flag not detected: %+v %v", viewed, err)
	}
	cfg.RunID = ""
	if _, err := coordinator.Resume(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "unanswered") {
		t.Fatalf("unanswered question auto-resumed: %v", err)
	}
	q := requests[0]
	if err := coordinator.ReplyParent(t.Context(), scope(cfg), q.AgentID, q.RunID, q.ID, "keep compatibility"); err != nil {
		t.Fatal(err)
	}
	// Restart once more between durable delivery and restoring the tool result.
	coordinator = agent.NewCoordinator(runner, agentsession.NewFileStore(dir))
	defer coordinator.Close()
	if err := coordinator.ReplyParent(t.Context(), scope(cfg), q.AgentID, q.RunID, q.ID, "keep compatibility"); err != nil {
		t.Fatal("answer redelivery not idempotent", err)
	}
	if _, err := coordinator.Resume(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	wait(t, coordinator, cfg.ID)
	if len(runner.snapshots) != 1 {
		t.Fatal("recovery invoked extra runs")
	}
	snapshot := runner.snapshots[0]
	if len(agent.PendingToolIDs(snapshot.Messages)) != 0 {
		t.Fatal("AskParent tool result not restored")
	}
	answer := snapshot.Messages[len(snapshot.Messages)-1].Content[0].OfToolResult
	if answer == nil || answer.ToolUseID != "ask-before-crash" || answer.Content[0].OfText.Text != "keep compatibility" {
		t.Fatalf("restored wrong answer: %+v", answer)
	}
	if err := coordinator.ReplyParent(t.Context(), scope(cfg), q.AgentID, q.RunID, q.ID, "late old answer"); err == nil {
		t.Fatal("old run answer targeted resumed child")
	}
	saved, err := coordinator.Get(t.Context(), cfg.ID, scope(cfg))
	if err != nil || len(saved.ParentRequests) != 1 || saved.ParentRequests[0].AnsweredAt == nil {
		t.Fatal("request history lost")
	}
}

func TestRecoveryAnswerDoesNotAuthorizeUncertainSiblingTool(t *testing.T) {
	store := agentsession.NewFileStore(t.TempDir())
	cfg := config()
	cfg.RunID = "old-run"
	now := time.Now()
	call := sdk.NewAssistantMessage(sdk.NewToolUseBlock("ask", map[string]any{}, "AskParent"), sdk.NewToolUseBlock("edit", map[string]any{}, "Edit"))
	record := &agent.Record{Config: cfg, Status: agent.AgentStatus{ID: cfg.ID, RunID: cfg.RunID, ParentID: cfg.ParentID, ParentSessionID: cfg.ParentSessionID, WorkDir: cfg.WorkDir, State: agent.AgentWaitingParent}, Messages: []sdk.MessageParam{call}, Context: []int{0}, Runs: []agent.RunRecord{{ID: cfg.RunID, State: agent.AgentWaitingParent}}, ParentRequests: []agent.ParentRequest{{ID: "request", AgentID: cfg.ID, RunID: cfg.RunID, ToolUseID: "ask", Answer: "yes", AnsweredAt: &now}}}
	if err := store.Save(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	runner := &historyRunner{}
	c := agent.NewCoordinator(runner, store)
	defer c.Close()
	cfg.RunID = ""
	if _, err := c.Resume(t.Context(), cfg); !errors.Is(err, agent.ErrUncertain) {
		t.Fatalf("uncertain sibling was replayed or hidden: %v", err)
	}
	if len(runner.snapshots) != 0 {
		t.Fatal("model called before reconciliation")
	}
}
