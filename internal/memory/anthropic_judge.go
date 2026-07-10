package memory

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
)

type AnthropicJudge struct {
	Client *cpanthropic.Client
	Model  string
}

func (j AnthropicJudge) JudgeMemory(ctx context.Context, input MemoryJudgementInput) (MemoryJudgement, error) {
	if j.Client == nil {
		return StaticJudge{}.JudgeMemory(ctx, input)
	}
	payload := map[string]any{
		"text":              input.Text,
		"source_session_id": input.SourceSessionID,
		"work_dir":          input.WorkDir,
		"existing_summary":  input.ExistingSummary,
		"explicit":          input.Explicit,
		"candidate_reason":  input.CandidateReason,
		"related_memories":  input.RelatedMemories,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return MemoryJudgement{}, err
	}
	message, err := j.Client.Create(ctx, cpanthropic.MessageRequest{
		Model:     j.Model,
		MaxTokens: 700,
		System:    memoryJudgePrompt(),
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(string(body)))},
		Thinking:  false,
		Stream:    false,
	})
	if err != nil {
		return MemoryJudgement{}, err
	}
	var judgement MemoryJudgement
	if err := json.Unmarshal([]byte(strings.TrimSpace(cpanthropic.TextFromMessage(message))), &judgement); err != nil {
		return MemoryJudgement{}, err
	}
	return judgement, nil
}

func memoryJudgePrompt() string {
	return "You are a memory classification service. Return only JSON with fields: should_store (boolean), kind (fact|preference|constraint|task|workflow), scope (session|project|global), suggested_tier (M1|M2|M3|M4|M5), confidence (0-1), canonical_text (string), tags (string array), reason (string). Store only durable, reusable information. Use M2 for current-session working context, M3 for project short-term memory, M4 for stable long-term preferences/facts/constraints, and M5 for reusable workflows."
}
