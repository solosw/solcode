package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NativeComputerUseBackend keeps the tool independent of the MCP transport.
type NativeComputerUseBackend interface {
	ListTools(context.Context) ([]MCPToolInfo, error)
	CallTool(context.Context, string, json.RawMessage) (*ContentBlock, error)
	Close() error
}

type nativeComputerUseTool struct {
	BaseTool
	backend NativeComputerUseBackend
}

type nativeComputerUseParams struct {
	Action    string          `json:"action"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

var nativeComputerUseActions = []string{"describe", "list_apps", "get_app_state", "query", "click", "perform_secondary_action", "scroll", "drag", "type_text", "press_key", "set_value"}

func NewNativeComputerUseTool(backend NativeComputerUseBackend) Tool {
	return &nativeComputerUseTool{backend: backend}
}

func (t *nativeComputerUseTool) Name() string { return ComputerUseToolName }
func (t *nativeComputerUseTool) Description() string {
	return `Operate desktop apps through the embedded open-computer-use native runtime (not robotgo).
Use list_apps then get_app_state with arguments.app to see the accessibility tree and screenshot. Refresh get_app_state before using element_index; prefer semantic elements over coordinate clicks.
Call ComputerUse directly, not through BatchTool/PTC, because native action results can include screenshot images.
Pass native MCP parameters in arguments. For an unfamiliar action call describe with arguments.tool set to its name to inspect the exact runtime schema. describe without arguments lists actions.
Read-only: describe, list_apps, query. get_app_state can launch/activate apps even with keep placement, so it and other actions may change the real desktop and require permission in auto mode. Never send, purchase, delete, approve, upload, or enter secrets without user authorization.
Discovered via Skill "computer-use" or ToolSearch. Runtime starts lazily and preserves state across calls.`
}
func (t *nativeComputerUseTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":    map[string]any{"type": "string", "enum": nativeComputerUseActions},
			"arguments": map[string]any{"type": "object", "description": `Native tool arguments, e.g. {"app":"TextEdit"}, or {"tool":"click"} for describe. Inspect describe before using unfamiliar native parameters.`},
		},
		"required": []string{"action"}, "additionalProperties": false,
	}
}
func parseNativeComputerUse(input json.RawMessage) (nativeComputerUseParams, error) {
	var p nativeComputerUseParams
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, err
	}
	// Unmarshal also rejects trailing JSON values, unlike a single Decode.
	if !json.Valid(input) {
		return p, fmt.Errorf("expected one JSON object")
	}
	found := false
	for _, action := range nativeComputerUseActions {
		if p.Action == action {
			found = true
			break
		}
	}
	if !found {
		return p, fmt.Errorf("unsupported action %q; use describe for the new native API (legacy robotgo actions are disabled)", p.Action)
	}
	if len(p.Arguments) == 0 {
		p.Arguments = json.RawMessage(`{}`)
	}
	if raw := bytes.TrimSpace(p.Arguments); len(raw) == 0 || raw[0] != '{' {
		return p, fmt.Errorf("arguments must be a JSON object")
	}
	return p, nil
}
func (t *nativeComputerUseTool) ValidateInput(_ context.Context, input json.RawMessage) error {
	_, err := parseNativeComputerUse(input)
	return err
}

func (t *nativeComputerUseTool) IsReadOnly(input json.RawMessage) bool {
	p, err := parseNativeComputerUse(input)
	if err != nil {
		return false
	}
	switch p.Action {
	case "describe", "list_apps", "query":
		return true
	}

	return false
}
func (t *nativeComputerUseTool) IsDestructive(input json.RawMessage) bool {
	return !t.IsReadOnly(input)
}
func (t *nativeComputerUseTool) IsConcurrencySafe(json.RawMessage) bool { return false }
func (t *nativeComputerUseTool) Close() error {
	if t.backend != nil {
		return t.backend.Close()
	}
	return nil
}
func (t *nativeComputerUseTool) Invoke(ctx context.Context, _ *UseContext, input json.RawMessage) (*ContentBlock, error) {
	p, err := parseNativeComputerUse(input)
	if err != nil {
		return ErrorResult("invalid ComputerUse parameters: " + err.Error()), nil
	}
	if t.backend == nil {
		return ErrorResult("native ComputerUse backend is not configured"), nil
	}
	if p.Action != "describe" {
		result, err := t.backend.CallTool(ctx, p.Action, p.Arguments)
		if err != nil {
			return ErrorResult("ComputerUse: " + err.Error()), nil
		}
		if result == nil {
			return ErrorResult("ComputerUse runtime returned no result"), nil
		}
		return result, nil
	}
	var args struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal(p.Arguments, &args); err != nil {
		return ErrorResult("invalid describe arguments: " + err.Error()), nil
	}
	infos, err := t.backend.ListTools(ctx)
	if err != nil {
		return ErrorResult("ComputerUse: " + err.Error()), nil
	}
	var summary strings.Builder
	for _, info := range infos {
		if args.Tool != "" {
			if info.ToolName != args.Tool {
				continue
			}
			raw, err := json.Marshal(info.InputSchema)
			if err != nil {
				return nil, err
			}
			return Result(info.ToolName + ": " + info.Description + "\narguments schema: " + string(raw)), nil
		}
		fmt.Fprintf(&summary, "- %s: %s\n", info.ToolName, strings.SplitN(info.Description, "\n", 2)[0])
	}
	if args.Tool != "" {
		return ErrorResult(fmt.Sprintf("native tool %q is unavailable on this platform", args.Tool)), nil
	}
	return Result(summary.String() + "Use describe with arguments.tool for one action's exact schema."), nil
}
