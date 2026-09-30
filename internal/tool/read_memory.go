package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const ReadMemoryToolName = "ReadMemory"

const (
	readMemoryDefaultLimit = 8
	readMemoryMaxLimit     = 25
)

// MemoryReadRequest asks the memory layer for entries relevant to a query.
type MemoryReadRequest struct {
	Query     string
	Kind      string
	Scope     string
	Limit     int
	SessionID string
	WorkDir   string
}

// MemoryEntry is one retrieved memory, flattened for display.
type MemoryEntry struct {
	Text  string
	Tier  string
	Kind  string
	Scope string
	Tags  []string
	Topic string
	// OtherSession is true when the entry came from a different session.
	OtherSession bool
}

// MemoryReadResult reports retrieved entries plus why results may be limited.
type MemoryReadResult struct {
	Entries []MemoryEntry
	// Beliefs is the current one-per-topic belief set when the query is empty
	// or explicitly asks for beliefs (app may fill this).
	Beliefs []string
	// CrossSessionAllowed is false when this session opted out of
	// cross-session memory, so only its own entries are visible.
	CrossSessionAllowed bool
	Note                string
}

// MemoryReader retrieves stored memories for the current session.
type MemoryReader interface {
	ReadMemory(ctx context.Context, req MemoryReadRequest) (MemoryReadResult, error)
}

// ReadMemoryParams is the input schema for the ReadMemory tool.
type ReadMemoryParams struct {
	Query string `json:"query,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type readMemoryTool struct {
	BaseTool
	reader MemoryReader
}

// NewReadMemoryTool creates the model-driven memory lookup tool.
func NewReadMemoryTool(reader MemoryReader) Tool {
	return &readMemoryTool{reader: reader}
}

func (t *readMemoryTool) Name() string { return ReadMemoryToolName }

func (t *readMemoryTool) Description() string {
	return `Search what earlier work saved with WriteMemory: this session's entries, plus
entries from earlier sessions when this session enabled cross-session memory.

Sessions with cross-session memory enabled already receive the most relevant entries
at start, so reach for this tool when you need more than that:
- before working out a build command, test layout, or project convention from scratch
- when a decision looks like it was already made and you want the recorded reason
- before saving a new entry, to see what is already remembered
- with an empty query: returns the current belief set (one active item per topic)
  plus recent entries

Pass a query describing what you need; leave it empty to list current beliefs and
the most relevant recent entries. Narrow with kind or scope only when you are sure
of them, since a wrong filter hides entries that do exist. Results tagged
other-session came from a different session than this one.

Memory is a note from past work, not ground truth. When an entry contradicts code you
just read, trust the code and save the correction with WriteMemory.`
}

func (t *readMemoryTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "What to look for, e.g. \"build and test commands\". Empty lists the most relevant recent entries.",
			},
			"kind": map[string]any{
				"type":        "string",
				"enum":        writeMemoryKinds,
				"description": "Optional filter: only return this kind of memory.",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        writeMemoryScopes,
				"description": "Optional filter: only return memories with this scope.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Max entries to return (default %d, max %d).", readMemoryDefaultLimit, readMemoryMaxLimit),
			},
		},
	}
}

func (t *readMemoryTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *readMemoryTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *readMemoryTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *readMemoryTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params ReadMemoryParams
	if len(strings.TrimSpace(string(input))) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return ErrorResult("invalid parameters: " + err.Error()), nil
		}
	}
	if t.reader == nil {
		return ErrorResult("memory is not enabled for this session"), nil
	}

	kind, err := normalizeEnumValue(params.Kind, writeMemoryKinds, "", "kind")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	scope, err := normalizeEnumValue(params.Scope, writeMemoryScopes, "", "scope")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = readMemoryDefaultLimit
	}
	if limit > readMemoryMaxLimit {
		limit = readMemoryMaxLimit
	}

	req := MemoryReadRequest{
		Query: strings.TrimSpace(params.Query),
		Kind:  kind,
		Scope: scope,
		Limit: limit,
	}
	if uctx != nil {
		req.SessionID = uctx.SessionID
		req.WorkDir = uctx.WorkDir
	}

	result, err := t.reader.ReadMemory(ctx, req)
	if err != nil {
		return ErrorResult("failed to read memory: " + err.Error()), nil
	}
	return Result(formatMemoryReadResult(result, req)), nil
}

func formatMemoryReadResult(result MemoryReadResult, req MemoryReadRequest) string {
	var b strings.Builder
	if len(result.Beliefs) > 0 {
		b.WriteString("Current beliefs (one per topic):\n")
		for _, line := range result.Beliefs {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "-") {
				b.WriteString("- ")
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	if len(result.Entries) == 0 {
		if len(result.Beliefs) == 0 {
			b.WriteString("No stored memory matched")
			if req.Query != "" {
				b.WriteString(fmt.Sprintf(" %q", req.Query))
			}
			b.WriteString(".")
		}
	} else {
		b.WriteString(fmt.Sprintf("%d stored %s", len(result.Entries), pluralizeMemory(len(result.Entries))))
		if req.Query != "" {
			b.WriteString(fmt.Sprintf(" for %q", req.Query))
		}
		b.WriteString(":\n")
		for i, entry := range result.Entries {
			b.WriteString(fmt.Sprintf("%d. [%s/%s", i+1, defaultIfEmpty(entry.Kind, "fact"), defaultIfEmpty(entry.Scope, "project")))
			if entry.Tier != "" {
				b.WriteString(" " + entry.Tier)
			}
			if entry.Topic != "" {
				b.WriteString(" topic:" + entry.Topic)
			}
			if entry.OtherSession {
				b.WriteString(" other-session")
			}
			b.WriteString("] " + oneLineMemory(entry.Text))
			if len(entry.Tags) > 0 {
				b.WriteString(" (tags: " + strings.Join(entry.Tags, ", ") + ")")
			}
			b.WriteString("\n")
		}
	}
	if !result.CrossSessionAllowed {
		b.WriteString("\nNote: this session opted out of cross-session memory, so only its own entries are visible.")
	}
	if note := strings.TrimSpace(result.Note); note != "" {
		b.WriteString("\n" + note)
	}
	return strings.TrimSpace(b.String())
}

func pluralizeMemory(count int) string {
	if count == 1 {
		return "memory"
	}
	return "memories"
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func oneLineMemory(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
