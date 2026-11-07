package engine

import (
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/contextcompression"
	contextmessage "github.com/solosw/solcode/internal/message"
)

func contextBudgetMessage(text string) sdk.MessageParam {
	return sdk.NewUserMessage(sdk.NewTextBlock(contextmessage.DynamicContextText("context-budget", text)))
}

func contextReminder(overview contextcompression.Overview, model, level string, tokens int) string {
	payload, _ := json.Marshal(struct {
		contextcompression.Overview
		Model         string `json:"model"`
		Level         string `json:"pressure_level"`
		RequestTokens int    `json:"request_tokens"`
		FixedTokens   int    `json:"system_and_tools_tokens"`
	}{overview, model, level, tokens, max(0, tokens-overview.ActiveTokens)})
	return fmt.Sprintf("Context budget reminder (%s). Snapshot before this reminder; list totals may exceed the bounded lists.\n%s\nUse the revision and inclusive start/end references above to compress older completed work directly; no acp_status call is needed when this snapshot is current. Write the summary yourself, preserving requirements, decisions, evidence, exact paths, failures and unfinished work. Suggested ranges indicate structural safety, not that the work is complete. Protected ranges (including enclosing tool exchanges) must stay. Keep unrelated cached prefixes unchanged. Use acp_status when state is uncertain, the model changes, references become stale, or you need other ranges/details; use decompress/search_context for original evidence. After a successful compress use its new revision; refresh status before selecting another range. compress calls no summarizer model.", level, payload)
}

func (e *Engine) appendContextNudge(state *contextcompression.State, messages []sdk.MessageParam, builder ContextBuilder, req BuildRequest) ([]sdk.MessageParam, error) {
	if e.config.MaxContextTokens <= 0 {
		return messages, nil
	}
	if err := state.Sync(messages); err != nil {
		return messages, err
	}
	overview := state.Overview(int(e.config.MaxContextTokens), 8)
	tokens := int(builder.EstimateContextTokens(req))
	previous := state.Nudge
	level := state.Nudge.Decide(e.config.ContextNudge, tokens, int(e.config.MaxContextTokens), overview)
	if level == "" {
		return messages, nil
	}
	withReminder := append(append([]sdk.MessageParam(nil), messages...), contextBudgetMessage(contextReminder(overview, req.Model, level, tokens)))
	req.Messages = withReminder
	if builder.EstimateContextTokens(req) >= e.config.MaxContextTokens {
		previous.Requests = state.Nudge.Requests
		state.Nudge = previous
		return messages, nil
	}
	return withReminder, nil
}
