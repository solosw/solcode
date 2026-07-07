package memory

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	cpanthropic "github.com/solosw/codeplus-agent/internal/anthropic"
)

type AnthropicExtractor struct {
	Client *cpanthropic.Client
	Model  string
}

func (e AnthropicExtractor) ExtractMemories(ctx context.Context, input ExtractionInput) ([]MemoryJudgement, error) {
	if e.Client == nil {
		return nil, nil
	}
	payload := map[string]any{
		"source_session_id":    input.SourceSessionID,
		"work_dir":             input.WorkDir,
		"previous_summary":     input.PreviousSummary,
		"new_summary":          input.NewSummary,
		"transcript":           input.Transcript,
		"original_transcript":  input.OriginalTranscript,
		"compacted_transcript": input.CompactedTranscript,
		"retained_transcript":  input.RetainedTranscript,
		"discarded_transcript": input.DiscardedTranscript,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	message, err := e.Client.Create(ctx, cpanthropic.MessageRequest{
		Model:     e.Model,
		MaxTokens: 1200,
		System:    memoryExtractorPrompt(),
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(string(body)))},
		Thinking:  false,
		Stream:    false,
	})
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(cpanthropic.TextFromMessage(message))
	if text == "" {
		return nil, nil
	}
	return parseMemoryJudgements(text)
}

func parseMemoryJudgements(text string) ([]MemoryJudgement, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if block := extractTaggedBlock(text, "memory"); block != "" {
		text = block
	}
	var judgements []MemoryJudgement
	if err := json.Unmarshal([]byte(text), &judgements); err != nil {
		return nil, err
	}
	return judgements, nil
}

func extractTaggedBlock(text, tag string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.TrimSpace(tag) == "" {
		return ""
	}
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"
	start := strings.Index(text, startTag)
	if start < 0 {
		return ""
	}
	start += len(startTag)
	end := strings.Index(text[start:], endTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}

func memoryExtractorPrompt() string {
	return strings.Join([]string{
		"You extract durable candidate memories from a compacted coding-agent session.",
		"Follow the Headroom-style inline memory extraction pattern: return a <memory>...</memory> block containing only a JSON array.",
		"Do not include any text before or after the <memory> block.",
		"Each JSON item must have: should_store (boolean), kind (fact|preference|constraint|task|workflow), scope (session|project|global), suggested_tier (M1|M2|M3|M4|M5), confidence (0-1), canonical_text (string), tags (string array), reason (string).",
		"Use compacted_transcript as the primary source because it is the post-headroom session that will remain in context.",
		"For tool-call memories, DO NOT merely list which tools were used. The priority is to reconstruct file-level engineering trace: for each edited/written/patched file, state the file path and what changed, including replaced symbols/strings, added behavior, removed behavior, config/default changes, tests added or updated, and any unresolved follow-up.",
		"Store tool names only when they explain a concrete file modification or validation result; avoid standalone memories like 'used Edit, Bash'.",
		"Also extract validation/build commands with their outcomes, important errors, and durable decisions/preferences/constraints.",
		"Do not include secrets, large raw logs, or transient chatter.",
		"Prefer concise project/session memories in M2 or M3; use M5 for reusable workflows; avoid M4 unless clearly stable and long-term.",
	}, " ")
}
