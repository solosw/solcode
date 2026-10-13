package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/solosw/solcode/internal/permission"
)

const (
	ProtocolVersion = 1

	MethodInitialize               = "initialize"
	MethodAuthenticate             = "authenticate"
	MethodSessionNew               = "session/new"
	MethodSessionLoad              = "session/load"
	MethodSessionPrompt            = "session/prompt"
	MethodSessionCancel            = "session/cancel"
	MethodSessionSetMode           = "session/set_mode"
	MethodSessionUpdate            = "session/update"
	MethodSessionRequestPermission = "session/request_permission"
	MethodFSReadTextFile           = "fs/read_text_file"
	MethodFSWriteTextFile          = "fs/write_text_file"

	StopReasonEndTurn   = "end_turn"
	StopReasonCancelled = "cancelled"
	StopReasonMaxTurn   = "max_turn_requests"
	StopReasonRefusal   = "refusal"

	PermissionAllowOnce    = "allow-once"
	PermissionAllowAlways  = "allow-always"
	PermissionRejectOnce   = "reject-once"
	PermissionRejectAlways = "reject-always"

	ToolCallPending    = "pending"
	ToolCallInProgress = "in_progress"
	ToolCallCompleted  = "completed"
	ToolCallFailed     = "failed"

	JSONRPCParseError     = -32700
	JSONRPCInvalidRequest = -32600
	JSONRPCMethodNotFound = -32601
	JSONRPCInvalidParams  = -32602
	JSONRPCInternalError  = -32603
)

type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *jsonrpcError) Error() string {
	if e == nil {
		return "jsonrpc error"
	}
	return e.Message
}

type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientInfo         ImplementationInfo `json:"clientInfo,omitempty"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities,omitempty"`
}

type ImplementationInfo struct {
	Name    string `json:"name,omitempty"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type ClientCapabilities struct {
	FS       *FSClientCapabilities `json:"fs,omitempty"`
	Terminal bool                  `json:"terminal,omitempty"`
}

type FSClientCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

type FSReadTextFileParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type FSReadTextFileResult struct {
	Content string `json:"content"`
}

type FSWriteTextFileParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

type InitializeResult struct {
	ProtocolVersion   int                `json:"protocolVersion"`
	AgentInfo         ImplementationInfo `json:"agentInfo"`
	AgentCapabilities AgentCapabilities  `json:"agentCapabilities"`
	AuthMethods       []AuthMethod       `json:"authMethods,omitempty"`
}

type AgentCapabilities struct {
	LoadSession         bool                `json:"loadSession"`
	PromptCapabilities  PromptCapabilities  `json:"promptCapabilities"`
	MCPCapabilities     MCPCapabilities     `json:"mcpCapabilities,omitempty"`
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities,omitempty"`
}

type PromptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type MCPCapabilities struct {
	HTTP bool `json:"http,omitempty"`
	SSE  bool `json:"sse,omitempty"`
}

type SessionCapabilities struct {
}

type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionNewParams struct {
	Cwd        string          `json:"cwd"`
	MCPServers []MCPServerSpec `json:"mcpServers,omitempty"`
}

type MCPServerSpec struct {
	Name    string         `json:"name"`
	Command string         `json:"command,omitempty"`
	Args    []string       `json:"args,omitempty"`
	Env     MCPEnvironment `json:"env,omitempty"`
}

// MCPEnvironment accepts both ACP's [{"name":"KEY","value":"VALUE"}]
// form and the map form emitted by older clients.
type MCPEnvironment map[string]string

func (e *MCPEnvironment) UnmarshalJSON(data []byte) error {
	var object map[string]string
	if err := json.Unmarshal(data, &object); err == nil {
		*e = MCPEnvironment(object)
		return nil
	}

	var entries []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("env must be an object or an array of name/value entries: %w", err)
	}
	env := make(MCPEnvironment, len(entries))
	for _, entry := range entries {
		if entry.Name == "" {
			return fmt.Errorf("env entry name is required")
		}
		env[entry.Name] = entry.Value
	}
	*e = env
	return nil
}

type SessionNewResult struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
}

type SessionLoadParams struct {
	SessionID  string          `json:"sessionId"`
	Cwd        string          `json:"cwd,omitempty"`
	MCPServers []MCPServerSpec `json:"mcpServers,omitempty"`
}

type SessionLoadResult struct {
	Modes *SessionModeState `json:"modes,omitempty"`
}

type SessionPromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

type SessionCancelParams struct {
	SessionID string `json:"sessionId"`
}

type SessionSetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type SessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

type SessionUpdate struct {
	SessionUpdate     string
	Content           *ContentBlock
	ToolCallID        string
	Title             string
	Kind              string
	Status            string
	RawInput          json.RawMessage
	RawOutput         json.RawMessage
	ToolContent       []ToolCallContent
	Locations         []ToolCallLocation
	PlanEntries       []PlanEntry
	AvailableCommands []AvailableCommand
	CurrentModeID     string
	Message           string
	Usage             *UsageUpdate
}

// PlanEntry is the client-facing representation of a TodoWrite item.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

// ToolCallContent is ACP tool-call content: regular content, file diffs, or terminals.
type ToolCallContent struct {
	Type       string        `json:"type"`
	Content    *ContentBlock `json:"content,omitempty"`
	Path       string        `json:"path,omitempty"`
	OldText    *string       `json:"oldText,omitempty"`
	NewText    string        `json:"newText,omitempty"`
	TerminalID string        `json:"terminalId,omitempty"`
}

// ToolCallLocation lets clients follow which file a tool call touches.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

func (c ToolCallContent) MarshalJSON() ([]byte, error) {
	switch strings.TrimSpace(c.Type) {
	case "diff":
		payload := map[string]any{
			"type":    "diff",
			"path":    c.Path,
			"newText": c.NewText,
		}
		// ACP uses null oldText for newly created files.
		if c.OldText == nil {
			payload["oldText"] = nil
		} else {
			payload["oldText"] = *c.OldText
		}
		return json.Marshal(payload)
	case "terminal":
		return json.Marshal(map[string]any{
			"type":       "terminal",
			"terminalId": c.TerminalID,
		})
	default:
		payload := map[string]any{"type": c.Type}
		if c.Type == "" {
			payload["type"] = "content"
		}
		if c.Content != nil {
			payload["content"] = c.Content
		}
		return json.Marshal(payload)
	}
}

func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	payload := map[string]any{"sessionUpdate": u.SessionUpdate}
	if u.Content != nil {
		payload["content"] = u.Content
	}
	if u.ToolCallID != "" {
		payload["toolCallId"] = u.ToolCallID
	}
	if u.Title != "" {
		payload["title"] = u.Title
	}
	if u.Kind != "" {
		payload["kind"] = u.Kind
	}
	if u.Status != "" {
		payload["status"] = u.Status
	}
	if len(u.RawInput) > 0 {
		payload["rawInput"] = u.RawInput
	}
	if len(u.RawOutput) > 0 {
		payload["rawOutput"] = u.RawOutput
	}
	if len(u.ToolContent) > 0 {
		payload["content"] = u.ToolContent
	}
	if len(u.Locations) > 0 {
		payload["locations"] = u.Locations
	}
	if len(u.PlanEntries) > 0 || u.SessionUpdate == "plan" || u.SessionUpdate == "agent_plan_update" {
		payload["entries"] = u.PlanEntries
	}
	if len(u.AvailableCommands) > 0 || u.SessionUpdate == "available_commands_update" {
		payload["availableCommands"] = u.AvailableCommands
	}
	if u.CurrentModeID != "" {
		payload["currentModeId"] = u.CurrentModeID
	}
	if u.Message != "" {
		payload["message"] = u.Message
	}
	if u.Usage != nil {
		// claude-agent-acp flattens used/size/cost onto the update object.
		payload["used"] = u.Usage.Used
		if u.Usage.Size > 0 {
			payload["size"] = u.Usage.Size
		}
		if u.Usage.Cost != nil {
			payload["cost"] = u.Usage.Cost
		}
	}
	return json.Marshal(payload)
}

func (u *SessionUpdate) UnmarshalJSON(data []byte) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if raw, ok := payload["sessionUpdate"]; ok {
		_ = json.Unmarshal(raw, &u.SessionUpdate)
	}
	if raw, ok := payload["toolCallId"]; ok {
		_ = json.Unmarshal(raw, &u.ToolCallID)
	}
	if raw, ok := payload["title"]; ok {
		_ = json.Unmarshal(raw, &u.Title)
	}
	if raw, ok := payload["kind"]; ok {
		_ = json.Unmarshal(raw, &u.Kind)
	}
	if raw, ok := payload["status"]; ok {
		_ = json.Unmarshal(raw, &u.Status)
	}
	if raw, ok := payload["rawInput"]; ok {
		u.RawInput = raw
	}
	if raw, ok := payload["rawOutput"]; ok {
		u.RawOutput = raw
	}
	if raw, ok := payload["locations"]; ok {
		_ = json.Unmarshal(raw, &u.Locations)
	}
	if raw, ok := payload["entries"]; ok {
		_ = json.Unmarshal(raw, &u.PlanEntries)
	}
	if raw, ok := payload["availableCommands"]; ok {
		_ = json.Unmarshal(raw, &u.AvailableCommands)
	}
	if raw, ok := payload["currentModeId"]; ok {
		_ = json.Unmarshal(raw, &u.CurrentModeID)
	}
	if raw, ok := payload["message"]; ok {
		_ = json.Unmarshal(raw, &u.Message)
	}
	// usage_update fields are flat on the update (claude-agent-acp wire shape).
	if _, hasUsed := payload["used"]; hasUsed || payloadHasKey(payload, "size") || payloadHasKey(payload, "cost") {
		usage := UsageUpdate{}
		if raw, ok := payload["used"]; ok {
			_ = json.Unmarshal(raw, &usage.Used)
		}
		if raw, ok := payload["size"]; ok {
			// Accept both number (claude-agent-acp) and legacy {current,limit} object.
			var sizeNum int64
			if err := json.Unmarshal(raw, &sizeNum); err == nil {
				usage.Size = sizeNum
			} else {
				var legacy struct {
					Current int64 `json:"current"`
					Limit   int64 `json:"limit"`
				}
				if err := json.Unmarshal(raw, &legacy); err == nil {
					usage.Used = legacy.Current
					usage.Size = legacy.Limit
				}
			}
		}
		if raw, ok := payload["cost"]; ok {
			var cost UsageCost
			if err := json.Unmarshal(raw, &cost); err == nil {
				usage.Cost = &cost
			}
		}
		u.Usage = &usage
	}
	if raw, ok := payload["usage"]; ok && u.Usage == nil {
		var usage UsageUpdate
		if err := json.Unmarshal(raw, &usage); err == nil {
			u.Usage = &usage
		}
	}
	if raw, ok := payload["content"]; ok {
		var content ContentBlock
		if err := json.Unmarshal(raw, &content); err == nil && content.Type != "" {
			u.Content = &content
		} else {
			_ = json.Unmarshal(raw, &u.ToolContent)
		}
	}
	return nil
}

// UsageUpdate mirrors claude-agent-acp usage_update fields.
// Wire shape (flattened onto the session update, not nested under "usage"):
//
//	{ "sessionUpdate": "usage_update", "used": <tokens>, "size": <window>, "cost"?: {amount, currency} }
//
// used = context occupancy; size = context window limit.
type UsageUpdate struct {
	Used int64      `json:"used"`
	Size int64      `json:"size,omitempty"`
	Cost *UsageCost `json:"cost,omitempty"`
}

// UsageCost is optional USD spend reported by some agents (claude-agent-acp).
type UsageCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

type AvailableCommandInput struct {
	Hint string `json:"hint,omitempty"`
}

type ContentBlock struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	Data        string          `json:"data,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	URI         string          `json:"uri,omitempty"`
	Name        string          `json:"name,omitempty"`
	Resource    json.RawMessage `json:"resource,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type ToolCallUpdate struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Status     string             `json:"status,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput,omitempty"`
	Content    []ToolCallContent  `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type RequestPermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func defaultPermissionOptions() []PermissionOption {
	return []PermissionOption{
		{OptionID: PermissionAllowOnce, Name: "Allow once", Kind: "allow_once"},
		{OptionID: PermissionAllowAlways, Name: "Allow always", Kind: "allow_always"},
		{OptionID: PermissionRejectOnce, Name: "Reject once", Kind: "reject_once"},
		{OptionID: PermissionRejectAlways, Name: "Reject always", Kind: "reject_always"},
	}
}

func payloadHasKey(payload map[string]json.RawMessage, key string) bool {
	_, ok := payload[key]
	return ok
}

func availableModes() []SessionMode {
	var modes []SessionMode
	for _, info := range permission.AvailableModes() {
		modes = append(modes, SessionMode{ID: string(info.ID), Name: info.Name, Description: info.Description})
	}
	return modes
}

func modeState(current permission.Mode) *SessionModeState {
	current = permission.NormalizeMode(current)
	if current == "" {
		current = permission.ModeAuto
	}
	return &SessionModeState{
		CurrentModeID:  string(current),
		AvailableModes: availableModes(),
	}
}
