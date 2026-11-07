package tool

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/solosw/solcode/internal/contextcompression"
)

// searchContextTool unifies reversible context, archival memory and session recall.
// Readers retain responsibility for their existing session/consent boundaries.
type searchContextTool struct {
	BaseTool
	memory  MemoryReader
	session SessionMemoryReader
}

func NewSearchContextTool(memory MemoryReader, session SessionMemoryReader) Tool {
	return &searchContextTool{memory: memory, session: session}
}

func (t *searchContextTool) Name() string { return SearchContextToolName }
func (t *searchContextTool) Description() string {
	return `Search prior work through one entry point. source defaults to all available sources:
- context: literal case-insensitive search of this run's original messages and compression summaries. Stable mN/bN references can be read with decompress; offset paginates these matches. No unrelated parent or sibling history is accessible.
- memory: durable facts, preferences, project rules and verified commands saved with WriteMemory or the organizer. Cross-session entries require this session's existing opt-in and are tagged other-session. Optional kind/scope filters apply only to this source.
- session_memory: fuzzy search of this project's chronological log in .solcode/solcode.md, scoped to the current session id, with newest first when query is empty.
Use a query before re-deriving a build command, project convention or earlier decision, and before saving duplicate memory. An empty query returns current beliefs and recent stored memories; context search is skipped because it needs a query. Limit is per source (defaults: context 10, memory 8, session_memory 5; caps: 50/25/25). Results identify each source and report unavailable or failed sources independently. Memory is a note from past work, not ground truth: trust the code when they conflict.`
}
func (t *searchContextTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":  map[string]any{"type": "string", "description": "Search text. Empty returns beliefs/recent memories; required for source=context."},
			"source": map[string]any{"type": "string", "enum": []string{"all", "context", "memory", "session_memory"}, "description": "Source to search (default all)."},
			"kind":   map[string]any{"type": "string", "enum": writeMemoryKinds, "description": "Optional archival-memory kind filter; applies only to memory results."},
			"scope":  map[string]any{"type": "string", "enum": writeMemoryScopes, "description": "Optional archival-memory scope filter; applies only to memory results."},
			"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Pagination offset for context matches only. Use source=context to continue that page."},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum results per source; memory/session_memory are capped at 25."},
		},
		"additionalProperties": false,
	}
}
func (t *searchContextTool) IsReadOnly(json.RawMessage) bool { return true }

// ContextControl synchronizes run-local state, so batch calls must be serialized.
func (t *searchContextTool) IsConcurrencySafe(json.RawMessage) bool { return false }

func (t *searchContextTool) Invoke(ctx context.Context, u *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var p struct {
		Query  string `json:"query"`
		Source string `json:"source"`
		Kind   string `json:"kind"`
		Scope  string `json:"scope"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if len(strings.TrimSpace(string(input))) > 0 {
		if err := json.Unmarshal(input, &p); err != nil {
			return ErrorResult("invalid parameters: " + err.Error()), nil
		}
	}
	source, err := normalizeEnumValue(p.Source, []string{"all", "context", "memory", "session_memory"}, "all", "source")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	kind, err := normalizeEnumValue(p.Kind, writeMemoryKinds, "", "kind")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	scope, err := normalizeEnumValue(p.Scope, writeMemoryScopes, "", "scope")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	query := strings.TrimSpace(p.Query)
	if p.Offset < 0 || p.Limit < 0 {
		return ErrorResult("offset and limit must not be negative"), nil
	}
	if source == "context" && query == "" {
		return ErrorResult("query is required for source=context"), nil
	}
	if source != "all" && source != "memory" && (kind != "" || scope != "") {
		return ErrorResult("kind/scope filters require source=memory or all"), nil
	}
	if source != "all" && source != "context" && p.Offset != 0 {
		return ErrorResult("offset applies only to source=context or all"), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID, workDir := "", ""
	if u != nil {
		sessionID, workDir = u.SessionID, u.WorkDir
	}
	results := map[string]any{}
	record := func(name string, data any, err error) {
		if err != nil {
			results[name] = map[string]any{"status": "error", "error": err.Error()}
			return
		}
		results[name] = map[string]any{"status": "ok", "data": data}
	}
	unavailable := func(name, reason string) { results[name] = map[string]any{"status": "unavailable", "note": reason} }
	if source == "all" || source == "context" {
		switch {
		case query == "":
			results["context"] = map[string]any{"status": "skipped", "note": "Provide a query to search original messages and compression summaries."}
		case u == nil || u.ContextControl == nil:
			unavailable("context", "context control is unavailable in this run")
		default:
			data, err := u.ContextControl(ctx, SearchContextToolName, contextcompression.Request{Query: query, Offset: p.Offset, Limit: p.Limit})
			record("context", data, err)
		}
	}
	if source == "all" || source == "memory" {
		if t.memory == nil {
			unavailable("memory", "memory is not enabled for this session")
		} else {
			req := MemoryReadRequest{Query: query, Kind: kind, Scope: scope, Limit: searchMemoryLimit(p.Limit, readMemoryDefaultLimit), SessionID: sessionID, WorkDir: workDir}
			data, err := t.memory.ReadMemory(ctx, req)
			record("memory", formatMemoryReadResult(data, req), err)
		}
	}
	if source == "all" || source == "session_memory" {
		if t.session == nil {
			unavailable("session_memory", "session recall is unavailable")
		} else {
			req := SessionMemoryReadRequest{Query: query, Limit: searchMemoryLimit(p.Limit, readSessionMemoryDefaultLimit), SessionID: sessionID, WorkDir: workDir}
			data, err := t.session.ReadSessionMemory(ctx, req)
			record("session_memory", formatSessionMemoryReadResult(data, req), err)
		}
	}
	b, err := json.Marshal(map[string]any{"sources": results})
	if err != nil {
		return nil, err
	}
	return Result(string(b)), nil
}

func searchMemoryLimit(limit, fallback int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > readMemoryMaxLimit {
		return readMemoryMaxLimit
	}
	return limit
}
