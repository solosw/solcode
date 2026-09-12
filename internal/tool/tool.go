// Package tool defines the Tool interface and supporting types for the solcode.
// The design references ClaudeCode Go's engine.Tool interface with safety annotations,
// and OpenCode's simpler BaseTool pattern.
package tool

import (
	"context"
	"encoding/json"
	"time"
)

// UseContext carries contextual information for a tool invocation.
// TextFileSystem provides client-owned text file access for a tool invocation.
// ACP sessions use it when the connected client advertises fs capabilities.
type TextFileSystem interface {
	CanReadTextFile() bool
	CanWriteTextFile() bool
	ReadTextFile(ctx context.Context, path string) (string, error)
	WriteTextFile(ctx context.Context, path, content string) error
}

type UseContext struct {
	SessionID string
	MessageID string
	WorkDir   string
	// SkillRoots are absolute skill package directories (Agent Skills roots).
	// Relative paths like scripts/… or references/… resolve against these
	// when they are not found under WorkDir (or prefer them when skill-shaped).
	SkillRoots       []string
	AgentID          string
	TodoPath         string
	FastModel        string
	Status           func(string)
	TaskRetryDelay   time.Duration
	TextFileSystem   TextFileSystem
	RecordFileChange func(ctx context.Context, change FileChange)
	// CaptureCheckpoint records turn-start file content for code-only rewind.
	// content == nil means the file did not exist.
	CaptureCheckpoint func(path string, content *string)
	AskUser           func(ctx context.Context, params AskUserParams) (map[string]string, error)
	// OnAgentProgress reports nested sub-agent lifecycle / tool activity to the UI.
	OnAgentProgress func(AgentProgressEvent)
}

// AgentProgressEvent describes a Task/Subagent progress update for interactive UIs.
type AgentProgressEvent struct {
	Kind            string // started | completed | failed | cancelled | retry | tool_start | tool_done
	AgentID         string
	ParentAgentID   string
	ParentToolUseID string
	TaskID          string
	Description     string
	ToolName        string
	ToolInput       string
	Output          string
	IsError         bool
}

// FileChange describes a successful file mutation for optional project-level
// knowledge recording. It deliberately excludes full diffs from persistence.
type FileChange struct {
	ToolName    string
	Path        string
	Description string
	Before      string
	After       string
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

// ImageResult builds a multimodal image content block (base64) with an optional
// text caption. The engine converts this into a tool_result with text + image.
func ImageResult(mimeType, data, caption string) *ContentBlock {
	return &ContentBlock{
		Type:     "image",
		MimeType: mimeType,
		Data:     data,
		Text:     caption,
	}
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
