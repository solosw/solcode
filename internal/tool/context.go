package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/solosw/solcode/internal/contextcompression"
)

const (
	CompressToolName      = "compress"
	DecompressToolName    = "decompress"
	SearchContextToolName = "search_context"
	ACPStatusToolName     = "acp_status"
)

type contextTool struct {
	BaseTool
	name string
}

func ContextTools(memory MemoryReader, session SessionMemoryReader) []Tool {
	return []Tool{&contextTool{name: CompressToolName}, &contextTool{name: DecompressToolName}, NewSearchContextTool(memory, session), &contextTool{name: ACPStatusToolName}}
}
func (t *contextTool) Name() string { return t.name }
func (t *contextTool) Description() string {
	switch t.name {
	case CompressToolName:
		return "Control your own context: replace a completed contiguous history range with a summary YOU write in this call. Use stable mN/bN references and revision from a current context-budget reminder or acp_status; endpoints are inclusive. Call acp_status only when state is uncertain, the model changed, references are stale, or you need other ranges/details. Preserve user requirements, decisions, exact paths, failures and unfinished work. Include older bN summaries when a higher-level fold is useful. No summarizer model is called. Original messages remain recoverable with decompress/search_context. Latest user request, recent messages and tool pairing boundaries are protected. Fold older completed work before context becomes full; keep unrelated cached prefix messages unchanged."
	case DecompressToolName:
		return "Read original evidence behind a bN compression block, with offset/limit pagination. deep=true reads through nested folds. By default this does not change history. restore=true replaces a visible summary with its children (deep=true restores original messages), subject to context budget. For bN, offset/limit count source messages (previews). For an mN source reference returned by search_context, offset/limit count characters in the exact serialized message (default 4000, max 8000). Follow next_offset until total_runes to read large evidence."
	default:
		return "Inspect current context budget, revision, stable mN message / bN summary references, safe range boundaries and protected messages. Use view=overview for a compact overview, view=ranges for safe ranges, or view=messages (default) for paginated references. A current context-budget reminder already supplies revision and safe ranges for direct compress. Refresh here when state is uncertain, the model changes, or references become stale."
	}
}
func (t *contextTool) InputSchema() map[string]any {
	props := map[string]any{}
	required := []string{}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	switch t.name {
	case CompressToolName:
		props["start"] = str("Inclusive first visible mN or bN reference")
		props["end"] = str("Inclusive last visible mN or bN reference")
		props["summary"] = str("Summary written by you; retain requirements, evidence and unfinished work")
		props["topic"] = str("Short topic")
		props["revision"] = map[string]any{"type": "integer", "description": "Revision from a current context-budget reminder or acp_status"}
		required = []string{"start", "end", "summary", "revision"}
	case ACPStatusToolName:
		props["view"] = map[string]any{"type": "string", "enum": []string{"overview", "ranges", "messages"}, "description": "Overview or safe ranges without message details; messages is the default paginated listing"}
		props["offset"] = map[string]any{"type": "integer", "minimum": 0}
		props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200}
	case DecompressToolName:
		props["block_id"] = str("bN block or mN source reference")
		props["restore"] = map[string]any{"type": "boolean"}
		props["deep"] = map[string]any{"type": "boolean"}
		required = []string{"block_id"}
		props["offset"] = map[string]any{"type": "integer", "minimum": 0}
		props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 8000}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func (t *contextTool) IsReadOnly(json.RawMessage) bool        { return true } // session view only, no project mutation
func (t *contextTool) IsConcurrencySafe(json.RawMessage) bool { return false }
func (t *contextTool) Invoke(ctx context.Context, u *UseContext, input json.RawMessage) (*ContentBlock, error) {
	if u == nil || u.ContextControl == nil {
		return ErrorResult("context control is unavailable in this run"), nil
	}
	var req contextcompression.Request
	if len(input) > 0 {
		if err := json.Unmarshal(input, &req); err != nil {
			return ErrorResult("invalid parameters: " + err.Error()), nil
		}
	}
	if t.name == CompressToolName && req.Revision == nil {
		return ErrorResult("revision is required; call acp_status"), nil
	}
	data, err := u.ContextControl(ctx, t.name, req)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode context result: %w", err)
	}
	return Result(string(b)), nil
}
