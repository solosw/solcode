// Package tool defines the Tool interface and supporting types for the codeplus-agent.
// The design references ClaudeCode Go's engine.Tool interface with safety annotations,
// and OpenCode's simpler BaseTool pattern.
package tool

import (
	"context"
	"encoding/json"
)

// UseContext carries contextual information for a tool invocation.
type UseContext struct {
	SessionID string
	MessageID string
	WorkDir   string
	AgentID   string
	TodoPath  string
	FastModel string
	AskUser   func(ctx context.Context, params AskUserParams) (map[string]string, error)
}

// ContentBlock represents a content block in Anthropic's message format.
type ContentBlock struct {
	Type      string `json:"type"` // text | image | tool_use | tool_result
	Text      string `json:"text,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`   // for image blocks
	Data      string `json:"data,omitempty"`        // base64-encoded image data
	ToolUseID string `json:"tool_use_id,omitempty"` // for tool_result blocks
}

// ToolInfo describes a tool's metadata for the LLM API.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"input_schema"`
}

// Result builds a single text content block for a successful invocation.
func Result(text string) *ContentBlock {
	return &ContentBlock{Type: "text", Text: text}
}

// ErrorResult builds a single text content block for a failed invocation.
func ErrorResult(msg string) *ContentBlock {
	return &ContentBlock{Type: "text", Text: msg, IsError: true}
}

// Tool defines the interface that every tool must implement.
// It combines OpenCode's simplicity (Info + Run) with ClaudeCode Go's
// safety annotations.
type Tool interface {
	// Metadata
	Name() string
	Description() string
	InputSchema() map[string]any

	// Execution
	Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error)

	// Safety annotations — sensible defaults provided by BaseTool.
	IsDestructive(input json.RawMessage) bool
	IsReadOnly(input json.RawMessage) bool
	IsConcurrencySafe(input json.RawMessage) bool

	// Optional
	Aliases() []string
	ValidateInput(ctx context.Context, input json.RawMessage) error
}
