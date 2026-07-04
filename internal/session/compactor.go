package session

import (
	"context"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

type SummaryWriter interface {
	Summarize(ctx context.Context, previous string, newContent string) (string, error)
}

type CompactOptions struct {
	MaxRecentTurns         int
	SummaryThresholdTokens int
}

type CompactResult struct {
	Summary  string
	Messages []sdk.MessageParam
	Changed  bool
}

func Compact(ctx context.Context, summary string, messages []sdk.MessageParam, writer SummaryWriter, opts CompactOptions) (CompactResult, error) {
	result := CompactResult{Summary: summary, Messages: append([]sdk.MessageParam(nil), messages...)}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if writer == nil || len(messages) == 0 {
		return result, nil
	}
	threshold := opts.SummaryThresholdTokens
	if threshold <= 0 {
		threshold = 60_000
	}
	if ApproxTokensFromMessages(messages) < threshold {
		return result, nil
	}
	keep := opts.MaxRecentTurns
	if keep <= 0 {
		keep = 20
	}
	cut := compactCutIndex(messages, keep)
	if cut <= 0 || cut >= len(messages) {
		return result, nil
	}
	oldMessages := messages[:cut]
	retained := append([]sdk.MessageParam(nil), messages[cut:]...)
	transcript := Transcript(oldMessages)
	if strings.TrimSpace(transcript) == "" {
		return result, nil
	}
	updated, err := writer.Summarize(ctx, summary, transcript)
	if err != nil {
		return result, err
	}
	updated = strings.TrimSpace(updated)
	if updated == "" {
		return result, nil
	}
	return CompactResult{Summary: updated, Messages: retained, Changed: true}, nil
}

func ApproxTokensFromMessages(messages []sdk.MessageParam) int {
	text := Transcript(messages)
	if text == "" {
		return 0
	}
	return len([]rune(text))/4 + 1
}

func Transcript(messages []sdk.MessageParam) string {
	var b strings.Builder
	for _, msg := range messages {
		role := string(msg.Role)
		for _, block := range msg.Content {
			text := contentBlockText(block)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if role != "" {
				b.WriteString(role)
				b.WriteString(": ")
			}
			b.WriteString(strings.TrimSpace(text))
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func compactCutIndex(messages []sdk.MessageParam, keepTurns int) int {
	if keepTurns <= 0 || len(messages) == 0 {
		return 0
	}
	userCount := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if string(messages[i].Role) == "user" {
			userCount++
			if userCount >= keepTurns {
				return i
			}
		}
	}
	return 0
}

func contentBlockText(block sdk.ContentBlockParamUnion) string {
	if block.OfText != nil {
		return block.OfText.Text
	}
	if block.OfToolResult != nil {
		return "[tool result]"
	}
	if block.OfToolUse != nil {
		return "[tool use: " + block.OfToolUse.Name + "]"
	}
	return ""
}
