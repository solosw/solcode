package engine

import (
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/contextcompression"
)

// prepareForcedCompression uses the current model's normal tool loop. There is
// no separate summarizer or history replacement outside the compress tool.
func (e *Engine) prepareForcedCompression(state *contextcompression.State, messages []sdk.MessageParam, builder ContextBuilder, req BuildRequest, explicit bool) ([]sdk.MessageParam, bool, error) {
	if !explicit {
		if e.config.MaxContextTokens <= 0 {
			return messages, false, nil
		}
		percent := e.config.CompactionTriggerPercent
		if percent <= 0 || percent > 100 {
			percent = 85
		}
		if builder.EstimateContextTokens(req) < e.config.MaxContextTokens*int64(percent)/100 {
			return messages, false, nil
		}
	}
	if err := state.Sync(messages); err != nil {
		return messages, false, err
	}
	overview := state.Overview(int(e.config.MaxContextTokens), 8)
	if overview.TotalCompressibleRanges == 0 {
		return messages, false, nil
	}
	tokens := int(builder.EstimateContextTokens(req))
	text := contextReminder(overview, req.Model, "required", tokens)
	text += "\nCompression is mandatory for this request. Call compress now using a current safe range and revision above. Write a complete summary yourself, preserving the active task, constraints, decisions, evidence, exact paths, verification and unfinished work. Do not call other tools or finish the task in this response. After the tool succeeds, continue the original task from the retained context; compression does not complete the user's request."
	withReminder := append(append([]sdk.MessageParam(nil), messages...), contextBudgetMessage(text))
	req.Messages = withReminder
	if e.config.MaxContextTokens > 0 && builder.EstimateContextTokens(req) >= e.config.MaxContextTokens {
		return messages, false, fmt.Errorf("context exceeds the configured budget for mandatory compress; history is preserved")
	}
	return withReminder, true, nil
}
