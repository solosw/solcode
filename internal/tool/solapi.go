package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/solosw/solcode/internal/agent"
)

const SolAPIToolName = "solapi"

type SolAPIRequest struct {
	Action string `json:"action"`
	Code   string `json:"code,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type solAPITool struct{ BaseTool }

func NewSolAPITool() Tool        { return &solAPITool{} }
func (*solAPITool) Name() string { return SolAPIToolName }
func (*solAPITool) Description() string {
	return "Controls only the current main-agent session. register installs a JavaScript listener that stays alive while the session is idle: use await sol.sleep(ms), sol.readText(projectRelativePath) (null if absent, max 1 MiB), and sol.trigger(prompt) when an event condition is met. No require/process/network or arbitrary tool access in listeners. cancel interrupts the current run and stops all its listeners and queued triggers without deleting history. history returns recent listener/trigger records (offset/limit). JS cannot choose a session ID or modify permissions. Listeners stop when the host closes; saved records remain."
}
func (*solAPITool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"action": map[string]any{"type": "string", "enum": []string{"register", "cancel", "history"}},
		"code":   map[string]any{"type": "string", "description": "register only: listener JavaScript, max 65536 bytes. sleep accepts 25..86400000 ms; trigger accepts a prompt up to 65536 bytes."},
		"offset": map[string]any{"type": "integer", "minimum": 0, "description": "history only: zero-based offset, newest first"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "history only: default 20"},
	}, "required": []string{"action"}, "additionalProperties": false}
}
func decodeSolAPI(input json.RawMessage) (SolAPIRequest, error) {
	var req SolAPIRequest
	if !json.Valid(input) {
		return req, fmt.Errorf("invalid solapi JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, err
	}
	switch req.Action {
	case "register":
		if strings.TrimSpace(req.Code) == "" || len(req.Code) > 65536 {
			return req, fmt.Errorf("register requires 1..65536 bytes of JavaScript")
		}
		if req.Offset != 0 || req.Limit != 0 {
			return req, fmt.Errorf("offset/limit are history-only")
		}
	case "cancel":
		if req.Code != "" || req.Offset != 0 || req.Limit != 0 {
			return req, fmt.Errorf("cancel accepts no code or history parameters")
		}
	case "history":
		if req.Code != "" || req.Offset < 0 || req.Limit < 0 || req.Limit > 100 {
			return req, fmt.Errorf("history requires offset >= 0 and limit 1..100")
		}
		if req.Limit == 0 {
			req.Limit = 20
		}
	default:
		return req, fmt.Errorf("action must be register, cancel, or history")
	}
	return req, nil
}
func (*solAPITool) ValidateInput(_ context.Context, input json.RawMessage) error {
	_, err := decodeSolAPI(input)
	return err
}
func (*solAPITool) IsReadOnly(input json.RawMessage) bool {
	req, err := decodeSolAPI(input)
	return err == nil && req.Action == "history"
}
func (*solAPITool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func SolAPIHasSessionScope(uctx *UseContext) bool {
	return uctx != nil && uctx.SessionID != "" && (uctx.AgentRole == "" || uctx.AgentRole == string(agent.AgentRoleMain))
}
func (*solAPITool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	if !SolAPIHasSessionScope(uctx) {
		return ErrorResult("solapi is available only to the main agent in its current session"), nil
	}
	req, err := decodeSolAPI(input)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	if uctx.SolAPI == nil {
		return ErrorResult("session trigger host is unavailable"), nil
	}
	data, err := uctx.SolAPI(ctx, req)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	output, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return Result(string(output)), nil
}
