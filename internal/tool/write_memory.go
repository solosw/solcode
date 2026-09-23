package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const WriteMemoryToolName = "WriteMemory"

// writeMemoryMaxRunes bounds a single memory entry so the model stores a
// distilled fact instead of pasting transcript or file content.
const writeMemoryMaxRunes = 1200

// MemoryWriteRequest is a model-authored memory entry. The tool package stays
// decoupled from internal/memory; the app layer maps this onto the store.
type MemoryWriteRequest struct {
	Text       string
	Kind       string
	Scope      string
	Tags       []string
	Importance float64
	Reason     string
	SessionID  string
	WorkDir    string
}

// MemoryWriteResult reports what the memory layer did with the request.
type MemoryWriteResult struct {
	ID     string
	Text   string
	Tier   string
	Kind   string
	Scope  string
	// Turn is the checkpoint turn recorded with the entry when known.
	Turn   int
	Stored bool
	Merged bool
	// Reason explains a rejection (e.g. sensitive content) or the merge target.
	Reason string
}

// MemoryWriter persists a model-authored memory entry.
type MemoryWriter interface {
	WriteMemory(ctx context.Context, req MemoryWriteRequest) (MemoryWriteResult, error)
}

// WriteMemoryParams is the input schema for the WriteMemory tool.
type WriteMemoryParams struct {
	Memory     string   `json:"memory"`
	Kind       string   `json:"kind,omitempty"`
	Scope      string   `json:"scope,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Importance float64  `json:"importance,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

var (
	writeMemoryKinds  = []string{"fact", "preference", "constraint", "task", "workflow"}
	writeMemoryScopes = []string{"session", "project", "global"}
)

type writeMemoryTool struct {
	BaseTool
	writer MemoryWriter
}

// NewWriteMemoryTool creates the model-driven memory write tool.
func NewWriteMemoryTool(writer MemoryWriter) Tool {
	return &writeMemoryTool{writer: writer}
}

func (t *writeMemoryTool) Name() string { return WriteMemoryToolName }

func (t *writeMemoryTool) Description() string {
	return `Save one durable fact to memory. You decide what is worth saving, and when.

Use this as a normal task-lifecycle action, not only when the user explicitly
asks you to remember something. Call it immediately after a meaningful milestone
creates durable knowledge, and before your final response if this task produced
such knowledge that has not already been saved.

Save it as soon as you learn something that would still be true and useful in a
future session:
- a user preference or convention (prefers table-driven tests, replies in Chinese)
- a project rule or invariant (never edit generated files, toolchain is pinned to Go 1.24)
- a verified command or repository layout (build: go build ./cmd/solcode; tests live in unit_tests/)
- a non-obvious decision, together with the reason it was made
- a recurring failure and its fix, or a reusable workflow

For substantial tasks, one to three focused entries are usually appropriate; do
not wait until the context is being compacted.

Do not save:
- transient state or in-flight steps of the current task (use TodoWrite)
- anything a quick read of the repo already makes obvious
- secrets, tokens, credentials, or personal data (these are rejected)
- code, diffs, logs, or transcript text; save the conclusion instead

How to write the entry:
- one or two plain sentences that stand on their own, with no pronouns pointing back
  at this conversation
- state the fact, not the story of how you found it
- to fix something remembered wrong, just save the corrected statement

Saving twice is safe: each WriteMemory call stores its own entry (duplicates are
kept), tagged with the current checkpoint turn when one is open. Entries are read
back with ReadMemory, and sessions that enabled cross-session memory also get the
relevant ones injected automatically at start.`
}

func (t *writeMemoryTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"memory": map[string]any{
				"type":        "string",
				"description": "The memory to store: one or two self-contained sentences, no code or transcript text.",
			},
			"kind": map[string]any{
				"type":        "string",
				"enum":        writeMemoryKinds,
				"description": "What sort of knowledge this is. Defaults to fact.",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        writeMemoryScopes,
				"description": "project (this repo, default), global (applies everywhere), session (only this session).",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Optional short retrieval keywords, e.g. [\"testing\", \"build\"].",
			},
			"importance": map[string]any{
				"type":        "number",
				"description": "Optional 0-1 importance. Use >0.8 only for constraints that must never be violated.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Optional short note on why this is worth remembering.",
			},
		},
		"required": []string{"memory"},
	}
}

func (t *writeMemoryTool) IsDestructive(_ json.RawMessage) bool { return false }
func (t *writeMemoryTool) IsReadOnly(_ json.RawMessage) bool    { return false }

// Memory writes touch a shared JSON store, so keep them serialized.
func (t *writeMemoryTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *writeMemoryTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params WriteMemoryParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	if t.writer == nil {
		return ErrorResult("memory is not enabled for this session"), nil
	}

	text := strings.TrimSpace(params.Memory)
	if text == "" {
		return ErrorResult("memory is required: pass one or two sentences worth remembering"), nil
	}
	if len([]rune(text)) > writeMemoryMaxRunes {
		return ErrorResult(fmt.Sprintf("memory is too long (%d runes, max %d): store the conclusion, not the raw content",
			len([]rune(text)), writeMemoryMaxRunes)), nil
	}
	kind, err := normalizeEnumValue(params.Kind, writeMemoryKinds, "fact", "kind")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	scope, err := normalizeEnumValue(params.Scope, writeMemoryScopes, "project", "scope")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}

	req := MemoryWriteRequest{
		Text:       text,
		Kind:       kind,
		Scope:      scope,
		Tags:       cleanMemoryTags(params.Tags),
		Importance: params.Importance,
		Reason:     strings.TrimSpace(params.Reason),
	}
	if uctx != nil {
		req.SessionID = uctx.SessionID
		req.WorkDir = uctx.WorkDir
	}

	result, err := t.writer.WriteMemory(ctx, req)
	if err != nil {
		return ErrorResult("failed to write memory: " + err.Error()), nil
	}
	return Result(formatMemoryWriteResult(result, req)), nil
}

func formatMemoryWriteResult(result MemoryWriteResult, req MemoryWriteRequest) string {
	stored := strings.TrimSpace(result.Text)
	if stored == "" {
		stored = req.Text
	}
	switch {
	case result.Merged:
		msg := fmt.Sprintf("Merged into an existing memory (%s/%s): %s", result.Kind, result.Scope, stored)
		if strings.TrimSpace(result.Reason) != "" {
			msg += "\nReason: " + result.Reason
		}
		return msg
	case !result.Stored:
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = "rejected by the memory gate"
		}
		return "Memory not stored: " + reason
	default:
		msg := fmt.Sprintf("Memory stored (%s/%s, tier %s): %s", result.Kind, result.Scope, result.Tier, stored)
		if result.Turn != 0 {
			msg += fmt.Sprintf("\nTurn: %d", result.Turn)
		}
		if len(req.Tags) > 0 {
			msg += "\nTags: " + strings.Join(req.Tags, ", ")
		}
		return msg
	}
}

func normalizeEnumValue(value string, allowed []string, fallback, field string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback, nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("invalid %s %q: expected one of %s", field, value, strings.Join(allowed, ", "))
}

func cleanMemoryTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
		if len(out) == 8 {
			break
		}
	}
	return out
}
