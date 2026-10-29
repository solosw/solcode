package acp

import (
	"encoding/json"
	"strings"

	"github.com/solosw/solcode/internal/tool"
)

// ACP v1's standard subagent extension is opt-in. In particular, true, null,
// and arrays are not capability objects (matching codex-acp's negotiation).
func supportsSubagents(raw json.RawMessage) bool {
	var capability map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &capability) == nil && capability != nil
}

type acpSubagent struct {
	session  *acpSession
	parentID string
	runID    string
	runs     map[string]bool
	finished bool
	hasText  bool
}

// emitSubagentProgress keeps child conversation identity across runs. Finishing
// foreground work sends idle, not deletion/closure of the child session. Calls
// are serialized through the parent so discovery always precedes child content,
// including events arriving from detached agents after session/prompt returns.
func (s *Server) emitSubagentProgress(root *acpSession, event tool.AgentProgressEvent) {
	// A rejected resume must not replace an already running child conversation.
	if event.Pending {
		return
	}
	root.subagentMu.Lock()
	defer root.subagentMu.Unlock()
	key := strings.TrimSpace(event.AgentID)
	if key == "" {
		key = strings.TrimSpace(event.TaskID)
	}
	if key == "" {
		return
	}
	if root.subagents == nil {
		root.subagents = make(map[string]*acpSubagent)
	}
	kind := strings.ToLower(strings.TrimSpace(event.Kind))
	child := root.subagents[key]
	fresh := child == nil
	if fresh {
		parentID := root.id
		if parent := root.subagents[event.ParentAgentID]; parent != nil {
			parentID = parent.session.id
		}
		child = &acpSubagent{parentID: parentID, runID: event.RunID, runs: map[string]bool{event.RunID: true}, session: &acpSession{
			id: s.nextSessionID(), workDir: root.workDir,
			toolCalls: make(map[string]string), toolCallsByID: make(map[string]string),
			lastToolInput: make(map[string]json.RawMessage),
		}}
		root.subagents[key] = child
		title := strings.TrimSpace(event.Description)
		if title == "" {
			title = key
		}
		s.emitUpdate(parentID, SessionUpdate{SessionUpdate: "subagent_update", SessionID: child.session.id,
			Title: title, Description: event.Description, Capabilities: &SubagentCapabilities{}})
	}
	if !fresh && kind == "started" {
		// A resumed conversation has a new run. Do not revive completed runs when
		// a duplicate/stale notification from that same run arrives.
		if event.RunID != "" && child.runs[event.RunID] {
			return
		}
		child.runs[event.RunID] = true
		child.runID, child.finished, child.hasText = event.RunID, false, false
		child.session.toolCalls = make(map[string]string)
		child.session.toolCallsByID = make(map[string]string)
		child.session.lastToolInput = make(map[string]json.RawMessage)
		child.session.toolTerminals = nil
	} else if (child.finished && kind != "retry") || (event.RunID != "" && child.runID != "" && event.RunID != child.runID) {
		return
	}
	if fresh || kind == "started" {
		s.emitSubagentState(child, &SubagentState{State: "running"})
	}
	toolID := event.ToolUseID
	if toolID != "" && child.runID != "" {
		toolID = child.runID + "/" + toolID
	}
	switch kind {
	case "text_delta":
		child.hasText = child.hasText || event.Output != ""
		s.emitText(child.session, "agent_message_chunk", event.Output)
	case "thinking_delta":
		s.emitText(child.session, "agent_thought_chunk", event.Output)
	case "tool_start":
		input := json.RawMessage(event.ToolInput)
		if len(input) > 0 && !json.Valid(input) {
			input, _ = json.Marshal(event.ToolInput)
		}
		s.emitToolStart(child.session, event.ToolName, input, toolID)
	case "tool_done":
		s.emitToolDone(child.session, event.ToolName, event.Output, event.IsError, toolID)
	case "retry":
		s.emitUpdate(child.session.id, SessionUpdate{SessionUpdate: "status_update", Message: event.Output})
	case "completed", "failed", "cancelled", "canceled":
		state := &SubagentState{State: "idle", StopReason: StopReasonEndTurn}
		if kind == "cancelled" || kind == "canceled" {
			state.StopReason = StopReasonCancelled
		} else if kind == "failed" || event.IsError {
			state.StopReason = "error"
			state.Error = &jsonrpcError{Code: JSONRPCInternalError, Message: event.Output}
		} else if !child.hasText {
			// Some runners only report a final result, rather than text deltas.
			s.emitText(child.session, "agent_message_chunk", event.Output)
		}
		child.finished = true
		s.emitSubagentState(child, state)
	}
}

func (s *Server) emitSubagentState(child *acpSubagent, state *SubagentState) {
	// v1 reports child state on the parent subagent_update. A top-level
	// state_update on the child is v2-only, and must not be sent here.
	s.emitUpdate(child.parentID, SessionUpdate{SessionUpdate: "subagent_update", SessionID: child.session.id, State: state})
}
