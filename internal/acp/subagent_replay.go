package acp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/session"
)

// replayPersistedSubagents reconstructs client discovery from durable child records.
// It only emits protocol updates: loading a conversation never resumes a worker.
func (s *Server) replayPersistedSubagents(ctx context.Context, root *acpSession) error {
	if !root.subagentsEnabled || root.application.Coordinator == nil {
		return nil
	}
	records, err := root.application.Coordinator.Records(ctx, agent.Scope{WorkDir: root.workDir})
	if err != nil {
		return fmt.Errorf("restore subagents: %w", err)
	}
	byID := make(map[string]*agent.Record, len(records))
	for _, record := range records {
		byID[string(record.Status.ID)] = record
	}
	root.subagentMu.Lock()
	defer root.subagentMu.Unlock()
	if root.subagents == nil {
		root.subagents = make(map[string]*acpSubagent)
	}
	// Parents are discovered before descendants even when storage lists the newest
	// child first. ParentSessionID on nested runs is the owning child agent id.
	for len(records) > 0 {
		remaining := make([]*agent.Record, 0, len(records))
		restored := false
		for _, record := range records {
			key := string(record.Status.ID)
			if key == "" || root.subagents[key] != nil {
				continue
			}
			runs := subagentReplayRuns(record)
			var visible []agent.RunRecord
			for _, run := range runs {
				if subagentRunBelongsToRoot(root, run, byID) {
					visible = append(visible, run)
				}
			}
			if len(visible) == 0 {
				remaining = append(remaining, record)
				continue
			}
			latest := visible[len(visible)-1]
			parentID := root.id
			if parent := root.subagents[latest.ParentSessionID]; parent != nil && latest.ParentSessionID != root.persistID() {
				parentID = parent.session.id
			}
			title := strings.TrimSpace(latest.Description)
			if title == "" {
				title = key
			}
			child := s.announceSubagent(root, key, parentID, latest.ID, title, latest.Description)
			visibleIDs := map[string]bool{}
			for _, run := range visible {
				visibleIDs[run.ID] = true
				child.runs[run.ID] = true
			}
			history := subagentReplayMessages(record, visibleIDs, len(visible) == len(runs))
			for _, run := range visible {
				hasText := false
				for _, update := range sessionHistoryUpdates(&session.Session{Messages: history[run.ID]}) {
					if update.ToolCallID != "" && run.ID != "" {
						update.ToolCallID = run.ID + "/" + update.ToolCallID
					}
					hasText = hasText || update.SessionUpdate == "agent_message_chunk"
					s.emitUpdate(child.session.id, update)
				}
				if !hasText && run.Result.Output != "" {
					s.emitText(child.session, "agent_message_chunk", run.Result.Output)
					hasText = true
				}
				if run.ID == latest.ID {
					child.hasText = hasText
				}
			}
			state := latest.State
			result := latest.Result
			if latest.ID == record.Status.RunID {
				state, result = record.Status.State, record.Result
			}
			// An older, unfinished run cannot be live once a later run superseded it.
			if latest.ID != record.Status.RunID && (state == agent.AgentRunning || state == agent.AgentWaitingPermission || state == agent.AgentWaitingParent) {
				state = agent.AgentInterrupted
			}
			restoredState := &SubagentState{State: "idle", StopReason: StopReasonEndTurn}
			child.finished = true
			switch state {
			case agent.AgentRunning, agent.AgentWaitingPermission, agent.AgentWaitingParent:
				restoredState = &SubagentState{State: "running"}
				child.finished = false
			case agent.AgentCancelled:
				restoredState.StopReason = StopReasonCancelled
			case agent.AgentFailed, agent.AgentInterrupted:
				message := strings.TrimSpace(result.Error)
				if state == agent.AgentInterrupted {
					message = "Previous run was interrupted; inspect history before resuming."
				}
				if message == "" {
					message = "Subagent run failed"
				}
				restoredState.StopReason = "error"
				restoredState.Error = &jsonrpcError{Code: JSONRPCInternalError, Message: message}
			}
			s.emitSubagentState(child, restoredState)
			if state == agent.AgentWaitingParent {
				s.emitUpdate(child.session.id, SessionUpdate{SessionUpdate: "status_update", Message: "Waiting for parent input"})
			}
			restored = true
		}
		if !restored {
			break
		}
		records = remaining
	}
	return nil
}

// A nested child belongs to the root that owned its parent at launch time.
// The same parent agent can later be reused by a different main session.
func subagentRunBelongsToRoot(root *acpSession, run agent.RunRecord, records map[string]*agent.Record) bool {
	if run.ParentSessionID == root.persistID() {
		return true
	}
	parentID := string(run.ParentID)
	parent := root.subagents[parentID]
	if parent == nil || run.ParentSessionID != parentID || records[parentID] == nil {
		return false
	}
	runs := subagentReplayRuns(records[parentID])
	allVisible := true
	var owner *agent.RunRecord
	for i := range runs {
		candidate := &runs[i]
		allVisible = allVisible && parent.runs[candidate.ID]
		if run.StartedAt.IsZero() || candidate.StartedAt.IsZero() || candidate.StartedAt.After(run.StartedAt) {
			continue
		}
		if owner == nil || !candidate.StartedAt.Before(owner.StartedAt) {
			owner = candidate
		}
	}
	if owner != nil {
		return parent.runs[owner.ID]
	}
	// Without timestamps, replay only unambiguous single-root ancestry.
	return allVisible
}

// Older snapshots may predate per-run records. Keep their latest conversation
// readable while still requiring an explicit parent-session association.
func subagentReplayRuns(record *agent.Record) []agent.RunRecord {
	if len(record.Runs) > 0 {
		return record.Runs
	}
	return []agent.RunRecord{{ID: record.Status.RunID, ParentID: record.Config.ParentID, ParentSessionID: record.Config.ParentSessionID, Description: record.Config.Description, State: record.Status.State, Result: record.Result, StartedAt: record.Status.StartedAt}}
}

// Checkpoint events attribute append-only audit messages to their producing run.
// Do not display the immutable inherited parent fork or runs from other sessions.
func subagentReplayMessages(record *agent.Record, visible map[string]bool, all bool) map[string][]sdk.MessageParam {
	out := map[string][]sdk.MessageParam{}
	previous := 0
	for _, event := range record.Events {
		end := event.MessageCount
		if end > len(record.Messages) {
			end = len(record.Messages)
		}
		if end <= previous {
			continue
		}
		if visible[event.RunID] {
			out[event.RunID] = append(out[event.RunID], record.Messages[previous:end]...)
		}
		previous = end
	}
	// Legacy files without audit events can only be attributed safely when every
	// recorded run belongs to this conversation.
	if len(record.Events) == 0 && all {
		out[record.Status.RunID] = record.Messages
	}
	return out
}
