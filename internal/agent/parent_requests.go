package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// ParentRequest is a durable, run-specific RPC. ToolUseID binds a reply to the
// already checkpointed call. Delivery is at-least-once; replies are idempotent.
type ParentRequest struct {
	ID          string     `json:"request_id"`
	AgentID     AgentID    `json:"agent_id"`
	RunID       string     `json:"run_id"`
	ToolUseID   string     `json:"tool_use_id"`
	Question    string     `json:"question"`
	Context     string     `json:"context,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	Answer      string     `json:"answer,omitempty"`
	AnsweredAt  *time.Time `json:"answered_at,omitempty"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
}

func ownsParent(status AgentStatus, scope Scope) bool {
	return filepath.Clean(status.WorkDir) == filepath.Clean(scope.WorkDir) && status.ParentSessionID == scope.ParentSessionID && status.ParentID == scope.ParentID
}

func (c *Coordinator) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// ParentActivity takes the wake subscription and state in one critical section,
// preventing a lost wake between checking the mailbox and parking the parent.
func (c *Coordinator) ParentActivity(scope Scope) (<-chan struct{}, bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	live, questions := false, false
	for _, a := range c.agents {
		if !active(a) || !ownsParent(a.status, scope) || a.status.State == AgentCancelled {
			continue
		}
		live = true
		if a.record != nil {
			for _, r := range a.record.ParentRequests {
				if r.RunID == a.status.RunID && r.AnsweredAt == nil && r.CancelledAt == nil {
					questions = true
				}
			}
		}
	}
	return c.changed, live, questions
}

// ParentQuestions also reads saved requests after restart. Project-wide history
// access does not grant another parent session the right to receive or answer RPCs.
func (c *Coordinator) ParentQuestions(ctx context.Context, scope Scope) ([]ParentRequest, error) {
	records, err := c.Records(ctx, scope)
	if err != nil {
		return nil, err
	}
	var out []ParentRequest
	for _, record := range records {
		if !ownsParent(record.Status, scope) || record.Archived || record.Status.State == AgentCancelled || record.Status.State == AgentIdle || record.Status.State == AgentFailed {
			continue
		}
		for _, r := range record.ParentRequests {
			if r.RunID == record.Status.RunID && r.AnsweredAt == nil && r.CancelledAt == nil {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (c *Coordinator) DrainParentQuestions(ctx context.Context, scope Scope) ([]ParentRequest, error) {
	requests, err := c.ParentQuestions(ctx, scope)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []ParentRequest
	for _, r := range requests {
		if !c.deliveredQuestions[r.ID] {
			c.deliveredQuestions[r.ID] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// AskParent never holds the registry/checkpoint lock while awaiting an answer.
// An answer is saved before the broadcast, including when it beats the waiter.
func (c *Coordinator) AskParent(ctx context.Context, id AgentID, runID, toolUseID, question, detail string) (string, error) {
	if strings.TrimSpace(question) == "" || len(question) > 16000 || len(detail) > 32000 || toolUseID == "" {
		return "", fmt.Errorf("question required (max 16000 bytes), context max 32000 bytes, and tool use identity required")
	}
	c.mu.RLock()
	a := c.agents[id]
	c.mu.RUnlock()
	if a == nil {
		return "", fmt.Errorf("child run is not active")
	}
	a.checkpointMu.Lock()
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		a.checkpointMu.Unlock()
		return "", err
	}
	if !active(a) || a.status.RunID != runID || a.status.State == AgentCancelled || a.record == nil || a.status.ParentID == "" {
		c.mu.Unlock()
		a.checkpointMu.Unlock()
		return "", fmt.Errorf("child run is not active or has no parent")
	}
	next, err := cloneRecord(a.record)
	var request ParentRequest
	if err == nil {
		for _, r := range next.ParentRequests {
			if r.RunID == runID && r.ToolUseID == toolUseID {
				request = r
				break
			}
		}
		if request.ID == "" {
			snapshot, activeErr := next.Active(a.fork)
			err = activeErr
			valid := false
			pending := false
			for _, id := range PendingToolIDs(snapshot.Messages) {
				if id == toolUseID {
					pending = true
				}
			}
			for _, m := range snapshot.Messages {
				for _, b := range m.Content {
					if b.OfToolUse != nil && b.OfToolUse.ID == toolUseID && b.OfToolUse.Name == "AskParent" {
						valid = true
					}
				}
			}
			if err == nil && (!valid || !pending) {
				err = fmt.Errorf("AskParent call must be checkpointed before requesting input")
			}
			if err == nil {
				request = ParentRequest{ID: NewID("request"), AgentID: id, RunID: runID, ToolUseID: toolUseID, Question: question, Context: detail, CreatedAt: time.Now()}
				next.ParentRequests = append(next.ParentRequests, request)
				next.Status = a.status
				next.Status.State = AgentWaitingParent
				next.Runs[len(next.Runs)-1].State = AgentWaitingParent
				next.UpdatedAt = time.Now()
				next.Events = append(next.Events, SessionEvent{RunID: runID, Kind: string(EventNeedsInput), At: next.UpdatedAt, MessageCount: len(next.Messages)})
				err = c.save(ctx, next)
				if err == nil {
					a.record = next
					a.status = next.Status
					c.signalLocked()
				}
			}
		}
	}
	handler := c.eventHandler
	status := a.status
	cfg := a.record.Config
	c.mu.Unlock()
	a.checkpointMu.Unlock()
	if err != nil {
		return "", err
	}
	c.emit(handler, Event{Kind: EventNeedsInput, Detached: cfg.Detached, Status: status, Description: cfg.Description, ParentToolUseID: cfg.ParentToolUseID, TaskID: cfg.TaskID, Result: AgentResult{AgentID: id, RunID: runID, Output: question}})
	for {
		c.mu.RLock()
		wake := c.changed
		var current ParentRequest
		if a.record != nil {
			for _, r := range a.record.ParentRequests {
				if r.ID == request.ID {
					current = r
					break
				}
			}
		}
		cancelled := a.status.State == AgentCancelled
		c.mu.RUnlock()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if cancelled || current.CancelledAt != nil {
			return "", context.Canceled
		}
		if current.AnsweredAt != nil {
			return current.Answer, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-a.done:
			return "", fmt.Errorf("child run ended while waiting for parent")
		case <-wake:
		}
	}
}

// ReplyParent requires the exact owning parent, run and request. Identical
// redelivery succeeds; a conflicting second answer is rejected. Inactive runs
// acquire the store lease so an external process cannot race a recovery reply.
func (c *Coordinator) ReplyParent(ctx context.Context, scope Scope, id AgentID, runID, requestID, answer string) error {
	if strings.TrimSpace(answer) == "" || len(answer) > 64000 || runID == "" || requestID == "" {
		return fmt.Errorf("run_id, request_id and answer are required (answer max 64000 bytes)")
	}
	c.mu.RLock()
	a := c.agents[id]
	c.mu.RUnlock()
	if a != nil {
		a.checkpointMu.Lock()
		defer a.checkpointMu.Unlock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	var record *Record
	var err error
	if active(a) {
		record, err = cloneRecord(a.record)
	} else {
		if c.store != nil {
			release, e := c.store.Acquire(ctx, id)
			if e != nil {
				return e
			}
			defer release()
		}
		record, err = c.load(ctx, id)
	}
	if err != nil {
		return err
	}
	if !ownsParent(record.Status, scope) {
		return fmt.Errorf("only the owning parent session may answer this request")
	}
	if record.Status.RunID != runID || record.Archived {
		return fmt.Errorf("request run is stale or archived")
	}
	if (active(a) && a.status.State == AgentCancelled) || record.Status.State == AgentCancelled {
		return fmt.Errorf("request run is cancelled")
	}
	for i := range record.ParentRequests {
		r := &record.ParentRequests[i]
		if r.ID != requestID || r.RunID != runID {
			continue
		}
		if r.CancelledAt != nil {
			return fmt.Errorf("request is cancelled")
		}
		if r.AnsweredAt != nil {
			if r.Answer == answer {
				return nil
			}
			return fmt.Errorf("request already answered with a different answer")
		}
		now := time.Now()
		r.Answer = answer
		r.AnsweredAt = &now
		record.UpdatedAt = now
		record.Events = append(record.Events, SessionEvent{RunID: runID, Kind: string(EventAnswered), At: now, MessageCount: len(record.Messages)})
		if active(a) {
			record.Status = a.status
			record.Status.State = AgentRunning
			for _, pending := range record.ParentRequests {
				if pending.RunID == runID && pending.AnsweredAt == nil && pending.CancelledAt == nil {
					record.Status.State = AgentWaitingParent
				}
			}
			record.Runs[len(record.Runs)-1].State = record.Status.State
		}
		if err = c.save(ctx, record); err != nil {
			return err
		}
		if a != nil {
			a.record = record
			if active(a) {
				a.status = record.Status
			}
		}
		c.signalLocked()
		return nil
	}
	return fmt.Errorf("parent request not found: %s", requestID)
}

// restoreParentAnswers completes known RPC results, preserving partial tool
// result messages and leaving every unrelated interrupted call unresolved.
func restoreParentAnswers(record *Record, snapshot Snapshot) (Snapshot, error) {
	pending := PendingToolIDs(snapshot.Messages)
	var blocks []sdk.ContentBlockParamUnion
	for _, id := range pending {
		for _, r := range record.ParentRequests {
			if r.ToolUseID != id || r.RunID != record.Status.RunID || r.CancelledAt != nil {
				continue
			}
			if r.AnsweredAt == nil {
				return Snapshot{}, fmt.Errorf("parent request %s is unanswered; reply before resuming", r.ID)
			}
			blocks = append(blocks, sdk.NewToolResultBlock(id, r.Answer, false))
			break
		}
	}
	if len(blocks) == 0 {
		return snapshot, nil
	}
	messages := append([]sdk.MessageParam(nil), snapshot.Messages...)
	if n := len(messages); n > 0 && string(messages[n-1].Role) == "user" && len(messages[n-1].Content) > 0 && messages[n-1].Content[0].OfToolResult != nil {
		last := messages[n-1]
		last.Content = append(append([]sdk.ContentBlockParamUnion(nil), last.Content...), blocks...)
		messages[n-1] = last
	} else {
		messages = append(messages, sdk.NewUserMessage(blocks...))
	}
	snapshot.Messages = messages
	return snapshot, nil
}
