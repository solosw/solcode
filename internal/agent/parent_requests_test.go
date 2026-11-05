package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

type parentSessionRunner func(context.Context, SessionRequest) AgentResult

func (f parentSessionRunner) Run(context.Context, AgentConfig) AgentResult {
	return AgentResult{Error: "persistent runner required"}
}
func (f parentSessionRunner) RunSession(ctx context.Context, r SessionRequest) AgentResult {
	return f(ctx, r)
}

func parentQuestionRunner(c **Coordinator, cycles int) parentSessionRunner {
	return func(ctx context.Context, req SessionRequest) AgentResult {
		messages := append([]sdk.MessageParam(nil), req.Snapshot.Messages...)
		for i := 0; i < cycles; i++ {
			id := fmt.Sprintf("ask-%s-%d", req.Config.ID, i)
			messages = append(messages, sdk.NewAssistantMessage(sdk.NewToolUseBlock(id, map[string]any{"question": "choose"}, "AskParent")))
			if err := req.Save(Checkpoint{Snapshot: Snapshot{Messages: messages}, Stage: "response"}); err != nil {
				return AgentResult{Error: err.Error()}
			}
			answer, err := (*c).AskParent(ctx, req.Config.ID, req.Config.RunID, id, "choose", fmt.Sprintf("round %d", i))
			if err != nil {
				return AgentResult{Error: err.Error()}
			}
			messages = append(messages, sdk.NewUserMessage(sdk.NewToolResultBlock(id, answer, false)))
			if err := req.Save(Checkpoint{Snapshot: Snapshot{Messages: messages}, Stage: "tool_result"}); err != nil {
				return AgentResult{Error: err.Error()}
			}
		}
		return AgentResult{Output: "done"}
	}
}

func TestParentRequestEarlyAnswersAndMultipleCycles(t *testing.T) {
	var c *Coordinator
	c = NewCoordinator(parentQuestionRunner(&c, 3))
	defer c.Close()
	scope := Scope{ParentID: "main", ParentSessionID: "session", WorkDir: t.TempDir()}
	answers := 0
	// Reply inside the event callback, before AskParent enters its wait select.
	c.SetEventHandler(func(e Event) {
		if e.Kind == EventNeedsInput {
			questions, err := c.ParentQuestions(t.Context(), scope)
			if err != nil || len(questions) != 1 {
				t.Errorf("questions=%+v err=%v", questions, err)
				return
			}
			q := questions[0]
			if err := c.ReplyParent(t.Context(), scope, q.AgentID, q.RunID, q.ID, "answer"); err != nil {
				t.Error(err)
			}
			answers++
		}
	})
	cfg := AgentConfig{ID: "child", RunID: "run", ParentID: scope.ParentID, ParentSessionID: scope.ParentSessionID, WorkDir: scope.WorkDir}
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	result, err := c.Wait(t.Context(), cfg.ID)
	if err != nil || result.Error != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if answers != 3 {
		t.Fatalf("cycles=%d", answers)
	}
	record, err := c.Get(t.Context(), cfg.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.ParentRequests) != 3 || record.Status.State != AgentIdle {
		t.Fatalf("record=%+v", record)
	}
	for _, r := range record.ParentRequests {
		if r.AnsweredAt == nil || r.Answer != "answer" {
			t.Fatal("answer not durable")
		}
	}
}

func TestParentRequestOwnershipIdempotenceAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var c *Coordinator
	c = NewCoordinator(parentQuestionRunner(&c, 2))
	defer c.Close()
	scope := Scope{ParentID: "main", ParentSessionID: "session", WorkDir: t.TempDir()}
	events := make(chan Event, 4)
	c.SetEventHandler(func(e Event) {
		if e.Kind == EventNeedsInput {
			events <- e
		}
	})
	cfg := AgentConfig{ID: "child", RunID: "run", ParentID: scope.ParentID, ParentSessionID: scope.ParentSessionID, WorkDir: scope.WorkDir}
	if _, err := c.Spawn(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	q, err := c.ParentQuestions(ctx, scope)
	if err != nil || len(q) != 1 {
		t.Fatalf("questions=%v err=%v", q, err)
	}
	record, _ := c.Get(ctx, cfg.ID, scope)
	if record.Status.State != AgentWaitingParent {
		t.Fatal("waiting state missing")
	}
	for _, bad := range []Scope{{ParentID: "main", ParentSessionID: "other", WorkDir: scope.WorkDir}, {ParentID: "other", ParentSessionID: "session", WorkDir: scope.WorkDir}, {ParentID: "main", ParentSessionID: "session", WorkDir: t.TempDir()}} {
		if err := c.ReplyParent(ctx, bad, cfg.ID, cfg.RunID, q[0].ID, "wrong"); err == nil {
			t.Fatal("foreign parent answered")
		}
	}
	if err := c.ReplyParent(ctx, scope, cfg.ID, "stale-run", q[0].ID, "wrong"); err == nil {
		t.Fatal("stale run accepted")
	}
	if err := c.ReplyParent(ctx, scope, cfg.ID, cfg.RunID, "wrong-request", "wrong"); err == nil {
		t.Fatal("unknown request accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.ReplyParent(ctx, scope, cfg.ID, cfg.RunID, q[0].ID, "same"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := c.ReplyParent(ctx, scope, cfg.ID, cfg.RunID, q[0].ID, "different"); err == nil {
		t.Fatal("conflicting second answer accepted")
	}
	select {
	case <-events:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	q, err = c.ParentQuestions(ctx, scope)
	if err != nil || len(q) != 1 {
		t.Fatalf("second question=%v %v", q, err)
	}
	if err := c.CancelRun(cfg.ID, cfg.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Wait(ctx, cfg.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.ReplyParent(ctx, scope, cfg.ID, cfg.RunID, q[0].ID, "late"); err == nil {
		t.Fatal("cancelled request accepted answer")
	}
	record, _ = c.Get(ctx, cfg.ID, scope)
	if record.ParentRequests[1].CancelledAt == nil || record.Status.State != AgentCancelled {
		t.Fatal("cancelled request not persisted")
	}
}

func TestParentWaitWakesForSiblingQuestion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var c *Coordinator
	asking := parentQuestionRunner(&c, 1)
	c = NewCoordinator(parentSessionRunner(func(ctx context.Context, req SessionRequest) AgentResult {
		if req.Config.ID == "quiet" {
			<-ctx.Done()
			return AgentResult{Error: ctx.Err().Error()}
		}
		return asking(ctx, req)
	}))
	defer c.Close()
	scope := Scope{ParentID: "main", ParentSessionID: "session", WorkDir: t.TempDir()}
	for _, id := range []AgentID{"quiet", "asking"} {
		if _, err := c.Spawn(ctx, AgentConfig{ID: id, RunID: "run", ParentID: scope.ParentID, ParentSessionID: scope.ParentSessionID, WorkDir: scope.WorkDir}); err != nil {
			t.Fatal(err)
		}
	}
	_, question, err := c.WaitRunOrQuestion(ctx, "quiet", "run", scope)
	if err != nil || question == nil || question.AgentID != "asking" {
		t.Fatalf("question=%+v err=%v", question, err)
	}
	if err := c.ReplyParent(ctx, scope, question.AgentID, question.RunID, question.ID, "continue"); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreParentAnswerKeepsUncertainSiblingCall(t *testing.T) {
	now := time.Now()
	r := &Record{Status: AgentStatus{RunID: "run"}, ParentRequests: []ParentRequest{{ID: "request", RunID: "run", ToolUseID: "ask", Answer: "answer", AnsweredAt: &now}}}
	snapshot := Snapshot{Messages: []sdk.MessageParam{sdk.NewAssistantMessage(sdk.NewToolUseBlock("ask", map[string]any{}, "AskParent"), sdk.NewToolUseBlock("mutation", map[string]any{}, "Edit"))}}
	out, err := restoreParentAnswers(r, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := PendingToolIDs(out.Messages); len(got) != 1 || got[0] != "mutation" {
		t.Fatalf("uncertain mutation disappeared: %v", got)
	}
	r.ParentRequests[0].AnsweredAt = nil
	if _, err := restoreParentAnswers(r, snapshot); err == nil || !strings.Contains(err.Error(), "unanswered") {
		t.Fatalf("unanswered recovery=%v", err)
	}
}
