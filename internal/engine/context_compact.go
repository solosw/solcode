package engine

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/contextcompression"
)

const forcedCompressionTimeout = 5 * time.Minute
const forcedCompressionMaxTokens int64 = 8192
const forcedCompressionSafetyTokens int64 = 1024

// createForcedCompression preserves the chat transport (including streaming),
// but bounds this maintenance request independently of the conversation lifetime.
// Tool arguments are progress, never user-facing answer text or committed history.
func (e *Engine) createForcedCompression(ctx context.Context, req cpanthropic.MessageRequest, inputTokens int64, timeout time.Duration) (*sdk.Message, error) {
	output := req.MaxTokens
	if output <= 0 || output > forcedCompressionMaxTokens {
		output = forcedCompressionMaxTokens
	}
	if e.config.MaxContextTokens > 0 {
		available := e.config.MaxContextTokens - inputTokens - forcedCompressionSafetyTokens
		if available < 1024 {
			return nil, fmt.Errorf("not enough context headroom for mandatory compress output; history is preserved; increase the configured context budget or reduce protected context")
		}
		if output > available {
			output = available
		}
	}
	req.MaxTokens = output
	started, lastProgress := time.Now(), time.Time{}
	received := 0
	progress := func(delta string) {
		received += len(delta)
		if e.config.OnStatus != nil && (lastProgress.IsZero() || time.Since(lastProgress) >= time.Second) {
			lastProgress = time.Now()
			e.config.OnStatus(fmt.Sprintf("Compacting... receiving summary (%d bytes, %s)", received, time.Since(started).Round(time.Second)))
		}
	}
	req.OnTextDelta, req.OnThinkingDelta, req.OnToolInputDelta = progress, progress, progress
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	message, err := e.config.Client.Create(requestCtx, req)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if requestCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("mandatory compress timed out after %s; history is preserved; check the provider or retry /compact: %w", timeout, context.DeadlineExceeded)
	}
	if err != nil {
		return nil, fmt.Errorf("mandatory compress request failed; history is preserved: %w", err)
	}
	if e.config.OnStatus != nil {
		e.config.OnStatus("Compacting... validating summary")
	}
	return message, nil
}

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
