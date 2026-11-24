package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

type v2Stream struct {
	kind      string
	messageID string
}

func (s *Server) registerV2Stream(id, root string) {
	s.v2UpdatesMu.Lock()
	defer s.v2UpdatesMu.Unlock()
	if root != "" {
		if _, open := s.v2Streams[root]; !open {
			return
		}
	}
	if s.v2Streams == nil {
		s.v2Streams = make(map[string]v2Stream)
	}
	s.v2Streams[id] = v2Stream{}
}

func (s *Server) discardV2Streams(id string) {
	s.v2UpdatesMu.Lock()
	defer s.v2UpdatesMu.Unlock()
	for streamID := range s.v2Streams {
		if streamID == id || strings.HasPrefix(streamID, id+"/agent/") {
			delete(s.v2Streams, streamID)
		}
	}
}

func (s *Server) notifyV2(id string, update map[string]any) {
	s.v2UpdatesMu.Lock()
	defer s.v2UpdatesMu.Unlock()
	stream, ok := s.v2Streams[id]
	if !ok {
		return
	}
	switch update["sessionUpdate"] {
	case "agent_message_chunk", "agent_thought_chunk":
		kind := update["sessionUpdate"].(string)
		if stream.kind != kind {
			stream = v2Stream{kind: kind, messageID: fmt.Sprintf("stream-%d-%d", s.v2StreamEpoch, s.v2MessageSeq.Add(1))}
		}
		update["messageId"] = stream.messageID
	case "state_update", "user_message", "tool_call_update", "replay_complete":
		stream = v2Stream{}
	}
	s.v2Streams[id] = stream
	if err := s.conn.Notify(MethodSessionUpdate, map[string]any{"sessionId": id, "update": update}); err != nil {
		log.Printf("acp v2 session update failed: %v", err)
	}
}

func (s *Server) emitV2Update(id string, u SessionUpdate) {
	switch u.SessionUpdate {
	case "current_mode_update", "available_commands_update":
		return
	case "user_message_chunk":
		if u.Content != nil {
			s.notifyV2(id, map[string]any{
				"sessionUpdate": "user_message", "messageId": fmt.Sprintf("history-%d-%d", s.v2StreamEpoch, s.v2MessageSeq.Add(1)),
				"content": []ContentBlock{*u.Content},
			})
		}
		return
	case "status_update":
		if u.Message != "" {
			s.notifyV2(id, map[string]any{"sessionUpdate": "notice", "severity": "info", "title": u.Message})
		}
		return
	case "plan", "agent_plan_update":
		entries := u.PlanEntries
		if entries == nil {
			entries = []PlanEntry{}
		}
		s.notifyV2(id, map[string]any{"sessionUpdate": "plan_update", "plan": map[string]any{
			"type": "items", "planId": "todo", "entries": entries,
		}})
		return
	case "config_option_update":
		s.notifyV2(id, map[string]any{"sessionUpdate": "config_option_update", "configOptions": v2ConfigOptions(u.ConfigOptions)})
		return
	case "usage_update":
		if u.Usage == nil || u.Usage.Size <= 0 {
			return
		}
	case "tool_call", "tool_call_update":
		u.SessionUpdate = "tool_call_update"
	case "subagent_update":
		u.Capabilities = nil
	}
	encoded, err := json.Marshal(u)
	if err != nil {
		log.Printf("encode ACP v2 session update: %v", err)
		return
	}
	var update map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&update); err != nil {
		log.Printf("decode ACP v2 session update: %v", err)
		return
	}
	if u.SessionUpdate == "tool_call_update" && len(u.ToolContent) > 0 {
		update["content"] = v2ToolContent(u.ToolContent)
	}
	s.notifyV2(id, update)
}

func (s *Server) callPermission(ctx context.Context, params RequestPermissionParams, result *RequestPermissionResult) error {
	if !s.isV2() {
		return s.conn.Call(ctx, MethodSessionRequestPermission, params, result)
	}
	call := params.ToolCall
	payload := map[string]any{
		"toolCallId": call.ToolCallID, "title": call.Title, "kind": call.Kind, "status": call.Status,
	}
	if len(call.RawInput) > 0 {
		payload["rawInput"] = call.RawInput
	}
	if len(call.Content) > 0 {
		payload["content"] = v2ToolContent(call.Content)
	}
	if len(call.Locations) > 0 {
		payload["locations"] = call.Locations
	}
	return s.conn.call(ctx, MethodSessionRequestPermission, map[string]any{
		"sessionId": params.SessionID, "title": call.Title, "options": params.Options,
		"subject": map[string]any{"type": "tool_call", "toolCall": payload},
	}, result, true)
}

func v2ToolContent(content []ToolCallContent) []any {
	blocks := make([]any, 0, len(content))
	for _, block := range content {
		switch block.Type {
		case "diff":
			operation := "modify"
			if block.OldText == nil {
				operation = "add"
			}
			blocks = append(blocks, map[string]any{"type": "diff", "changes": []map[string]string{{
				"operation": operation, "path": block.Path,
			}}})
		case "terminal":
			// ACP v2 runs tools in the agent process.
		default:
			blocks = append(blocks, block)
		}
	}
	return blocks
}
