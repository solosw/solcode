package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const WriteSessionMemoryToolName = "WriteSessionMemory"

const sessionMemoryMaxRunes = 2000

// SessionMemoryWriteRequest is a model-authored session memory. The tool package
// stays decoupled from internal/sessionmemory; the app layer maps this onto the
// solcode.md store.
type SessionMemoryWriteRequest struct {
	Keywords   []string
	Summary    string
	Importance float64
	SessionID  string
	WorkDir    string
}

// SessionMemoryWriteResult reports the stored record, including the checkpoint
// turn and changed files the app attached automatically.
type SessionMemoryWriteResult struct {
	Stored     bool
	Turn       int
	Files      []string
	Time       string
	SessionID  string
	Importance float64
	Path       string
	Reason     string
}

// SessionMemoryWriter persists a model-authored session memory.
type SessionMemoryWriter interface {
	WriteSessionMemory(ctx context.Context, req SessionMemoryWriteRequest) (SessionMemoryWriteResult, error)
}

// WriteSessionMemoryParams is the input schema for the WriteSessionMemory tool.
type WriteSessionMemoryParams struct {
	Keywords   []string `json:"keywords"`
	Summary    string   `json:"summary"`
	Importance float64  `json:"importance,omitempty"`
}

type writeSessionMemoryTool struct {
	BaseTool
	writer SessionMemoryWriter
}

// NewWriteSessionMemoryTool creates the session-memory write tool.
func NewWriteSessionMemoryTool(writer SessionMemoryWriter) Tool {
	return &writeSessionMemoryTool{writer: writer}
}

func (t *writeSessionMemoryTool) Name() string { return WriteSessionMemoryToolName }

func (t *writeSessionMemoryTool) Description() string {
	return `Append a session memory to this project's solcode.md (.solcode/solcode.md) when a
session ends. This is the session log: what this session set out to do, what it
actually did, and anything the next session needs to pick up.

When to use this vs WriteMemory:
- WriteSessionMemory (this tool) — one entry per session, written at the end, as a
  chronological record: the work done, decisions made, dead ends, and what remains.
  It is scoped to this session and stores the checkpoint turn, the files that
  changed, the timestamp, and the session id.
- WriteMemory — a single durable fact that stays true across sessions: a user
  preference, a project rule, a verified command, a settled decision. It is not a
  log; it is knowledge, and relevant entries are injected automatically in later
  sessions.

Rule of thumb: if it is "what happened in this session", use WriteSessionMemory; if
it is "a fact worth knowing in every future session", use WriteMemory. Most
sessions produce one session memory and zero to three WriteMemory entries.

The entry stores:
- keywords: short retrieval terms later sessions search by
- summary: what was done, decided, or learned, and anything the next session needs
- importance: 0-1, how much this should stand out later

The runtime appends what you do not provide: the checkpoint turn, the files changed
this session, the timestamp, and the session id. Every session memory for this
project lives in that one file, newest last.

Prefer outcomes over narration: a verified command, a settled decision and its
reason, a gotcha and its fix. Do not record secrets, and do not duplicate facts
already saved with WriteMemory.`
}

func (t *writeSessionMemoryTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"keywords": map[string]any{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Short retrieval keywords, e.g. [\"checkpoint\", \"rewind\"].",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "What this session did or learned, in a few sentences.",
			},
			"importance": map[string]any{
				"type":        "number",
				"description": "0-1 importance. Use >0.8 only for constraints that must never be violated.",
			},
		},
		"required": []string{"summary"},
	}
}

func (t *writeSessionMemoryTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *writeSessionMemoryTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *writeSessionMemoryTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *writeSessionMemoryTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params WriteSessionMemoryParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	if t.writer == nil {
		return ErrorResult("session memory is not enabled for this session"), nil
	}
	summary := strings.TrimSpace(params.Summary)
	if summary == "" {
		return ErrorResult("summary is required: describe what this session did or learned"), nil
	}
	if len([]rune(summary)) > sessionMemoryMaxRunes {
		return ErrorResult(fmt.Sprintf("summary is too long (%d runes, max %d)", len([]rune(summary)), sessionMemoryMaxRunes)), nil
	}

	req := SessionMemoryWriteRequest{
		Keywords:   cleanMemoryTags(params.Keywords),
		Summary:    summary,
		Importance: params.Importance,
	}
	if uctx != nil {
		req.SessionID = uctx.SessionID
		req.WorkDir = uctx.WorkDir
	}

	result, err := t.writer.WriteSessionMemory(ctx, req)
	if err != nil {
		return ErrorResult("failed to write session memory: " + err.Error()), nil
	}
	return Result(formatSessionMemoryWriteResult(result, req)), nil
}

func formatSessionMemoryWriteResult(result SessionMemoryWriteResult, req SessionMemoryWriteRequest) string {
	if !result.Stored {
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = "not stored"
		}
		return "Session memory not stored: " + reason
	}
	var b strings.Builder
	b.WriteString("Session memory stored")
	if result.Path != "" {
		b.WriteString(" in " + result.Path)
	}
	b.WriteString(fmt.Sprintf(" (turn %d", result.Turn))
	if strings.TrimSpace(result.Time) != "" {
		b.WriteString(", " + result.Time)
	}
	if result.SessionID != "" {
		b.WriteString(", session " + result.SessionID)
	}
	b.WriteString(fmt.Sprintf(", importance %.2f)", result.Importance))
	if len(result.Files) > 0 {
		b.WriteString("\nFiles: " + strings.Join(result.Files, ", "))
	} else if len(req.Keywords) > 0 {
		b.WriteString("\nKeywords: " + strings.Join(req.Keywords, ", "))
	}
	return b.String()
}
