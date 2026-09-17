package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const ReadSessionMemoryToolName = "ReadSessionMemory"

const (
	readSessionMemoryDefaultLimit = 5
	readSessionMemoryMaxLimit     = 25
)

// SessionMemoryReadRequest asks for session memories, optionally filtered by a
// fuzzy query.
type SessionMemoryReadRequest struct {
	Query     string
	Limit     int
	SessionID string
	WorkDir   string
}

// SessionMemoryEntry is one session memory, flattened for display.
type SessionMemoryEntry struct {
	Keywords   []string
	Summary    string
	Importance float64
	Turn       int
	Files      []string
	Time       string
	SessionID  string
}

// SessionMemoryReadResult reports retrieved entries.
type SessionMemoryReadResult struct {
	Entries []SessionMemoryEntry
	Path    string
}

// SessionMemoryReader retrieves session memories.
type SessionMemoryReader interface {
	ReadSessionMemory(ctx context.Context, req SessionMemoryReadRequest) (SessionMemoryReadResult, error)
}

// ReadSessionMemoryParams is the input schema for the ReadSessionMemory tool.
type ReadSessionMemoryParams struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type readSessionMemoryTool struct {
	BaseTool
	reader SessionMemoryReader
}

// NewReadSessionMemoryTool creates the session-memory lookup tool.
func NewReadSessionMemoryTool(reader SessionMemoryReader) Tool {
	return &readSessionMemoryTool{reader: reader}
}

func (t *readSessionMemoryTool) Name() string { return ReadSessionMemoryToolName }

func (t *readSessionMemoryTool) Description() string {
	return `Read this project's session memories from .solcode/solcode.md.

Two ways to use it:
- Pass a query to fuzzy-search by keyword or phrase, e.g. "checkpoint rewind" or
  "build command".
- Leave the query empty to get the most recent memories, newest first.

When to use this vs ReadMemory:
- ReadSessionMemory (this tool) — a chronological log of what earlier sessions did:
  "has someone worked on this area before, and what did they change or decide?"
- ReadMemory — durable facts saved with WriteMemory: preferences, project rules,
  verified commands. Those are also injected automatically at session start.

Reach for this before re-deriving what an earlier session already worked out.
Memories are notes, not ground truth: when one contradicts code you just read,
trust the code.`
}

func (t *readSessionMemoryTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Keywords to search for. Empty returns the most recent memories.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Max entries to return (default %d, max %d).", readSessionMemoryDefaultLimit, readSessionMemoryMaxLimit),
			},
		},
	}
}

func (t *readSessionMemoryTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *readSessionMemoryTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *readSessionMemoryTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *readSessionMemoryTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params ReadSessionMemoryParams
	if len(strings.TrimSpace(string(input))) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return ErrorResult("invalid parameters: " + err.Error()), nil
		}
	}
	if t.reader == nil {
		return ErrorResult("session memory is not enabled for this session"), nil
	}
	limit := params.Limit
	if limit <= 0 {
		limit = readSessionMemoryDefaultLimit
	}
	if limit > readSessionMemoryMaxLimit {
		limit = readSessionMemoryMaxLimit
	}
	req := SessionMemoryReadRequest{
		Query: strings.TrimSpace(params.Query),
		Limit: limit,
	}
	if uctx != nil {
		req.SessionID = uctx.SessionID
		req.WorkDir = uctx.WorkDir
	}
	result, err := t.reader.ReadSessionMemory(ctx, req)
	if err != nil {
		return ErrorResult("failed to read session memory: " + err.Error()), nil
	}
	return Result(formatSessionMemoryReadResult(result, req)), nil
}

func formatSessionMemoryReadResult(result SessionMemoryReadResult, req SessionMemoryReadRequest) string {
	var b strings.Builder
	if len(result.Entries) == 0 {
		if strings.TrimSpace(req.Query) != "" {
			fmt.Fprintf(&b, "No session memory matched %q", req.Query)
		} else {
			b.WriteString("No session memories recorded yet")
		}
		if result.Path != "" {
			fmt.Fprintf(&b, " (file: %s)", result.Path)
		}
		b.WriteString(".")
		return b.String()
	}

	fmt.Fprintf(&b, "%d session %s", len(result.Entries), pluralizeMemory(len(result.Entries)))
	if strings.TrimSpace(req.Query) != "" {
		fmt.Fprintf(&b, " for %q", req.Query)
	}
	b.WriteString(":\n")
	for i, entry := range result.Entries {
		fmt.Fprintf(&b, "\n%d. ", i+1)
		if strings.TrimSpace(entry.Time) != "" {
			b.WriteString(entry.Time)
		}
		fmt.Fprintf(&b, " · turn %d · importance %.2f", entry.Turn, entry.Importance)
		if entry.SessionID != "" {
			b.WriteString(" · session " + entry.SessionID)
		}
		b.WriteString("\n")
		b.WriteString(oneLineMemory(entry.Summary))
		b.WriteString("\n")
		if len(entry.Keywords) > 0 {
			b.WriteString("   keywords: " + strings.Join(entry.Keywords, ", ") + "\n")
		}
		if len(entry.Files) > 0 {
			b.WriteString("   files: " + strings.Join(entry.Files, ", ") + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}
