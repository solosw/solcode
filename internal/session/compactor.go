package session

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/codeplus-agent/internal/tokenest"
	headroom "github.com/superops-team/headroom-go"
)

type SummaryWriter interface {
	Summarize(ctx context.Context, previous string, newContent string) (string, error)
}

type CompactOptions struct {
	MaxRecentTurns         int
	SummaryThresholdTokens int
	TargetTokens           int
	EstimatedTokens        int
}

type CompactResult struct {
	Summary             string
	Messages            []sdk.MessageParam
	OriginalTranscript  string
	CompactedTranscript string
	RetainedTranscript  string
	DiscardedTranscript string
	Changed             bool
}

func Compact(ctx context.Context, summary string, messages []sdk.MessageParam, _ SummaryWriter, opts CompactOptions) (CompactResult, error) {
	result := CompactResult{Summary: summary, Messages: append([]sdk.MessageParam(nil), messages...)}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	working := compactionMessages(summary, messages)
	if len(working) == 0 {
		return result, nil
	}

	threshold := opts.SummaryThresholdTokens
	if threshold <= 0 {
		threshold = 60_000
	}
	estimated := opts.EstimatedTokens
	if estimated <= 0 {
		estimated = ApproxTokensFromMessages(working)
	}
	if estimated < threshold {
		return result, nil
	}
	keep := opts.MaxRecentTurns
	if keep <= 0 {
		keep = 20
	}
	target := opts.TargetTokens
	if target <= 0 {
		target = threshold / 2
		if target < 1 {
			target = 1
		}
	}

	originalTranscript := Transcript(working)
	if strings.TrimSpace(originalTranscript) == "" {
		return result, nil
	}
	compressedMessages, err := compressMessagesWithHeadroom(working, target)
	if err != nil {
		return result, err
	}
	if len(compressedMessages) == 0 {
		compressedMessages = append([]sdk.MessageParam(nil), working...)
	}
	compactedTranscript := Transcript(compressedMessages)
	retained := append([]sdk.MessageParam(nil), compressedMessages...)
	var discarded []sdk.MessageParam
	if ApproxTokensFromMessages(retained) > target {
		cut := compactCutIndex(retained, keep)
		targetCut := targetCutIndex(retained, target)
		if targetCut > cut {
			cut = targetCut
		}
		if cut > 0 && cut < len(retained) {
			discarded = append([]sdk.MessageParam(nil), retained[:cut]...)
			retained = append([]sdk.MessageParam(nil), retained[cut:]...)
		}
	}
	retainedTranscript := Transcript(retained)
	changed := sessionTranscript(messages) != retainedTranscript
	if !changed {
		return CompactResult{
			Summary:             summary,
			Messages:            append([]sdk.MessageParam(nil), messages...),
			OriginalTranscript:  originalTranscript,
			CompactedTranscript: compactedTranscript,
			RetainedTranscript:  retainedTranscript,
			DiscardedTranscript: Transcript(discarded),
			Changed:             false,
		}, nil
	}
	return CompactResult{
		Summary:             "",
		Messages:            retained,
		OriginalTranscript:  originalTranscript,
		CompactedTranscript: compactedTranscript,
		RetainedTranscript:  retainedTranscript,
		DiscardedTranscript: Transcript(discarded),
		Changed:             true,
	}, nil
}

func ApproxTokensFromMessages(messages []sdk.MessageParam) int {
	return tokenest.Messages(messages)
}

func Transcript(messages []sdk.MessageParam) string {
	return tokenest.Transcript(messages)
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

func previousTurnBoundary(messages []sdk.MessageParam, start int) int {
	if start <= 0 {
		return 0
	}
	for i := start - 1; i >= 0; i-- {
		if string(messages[i].Role) == "user" {
			return i
		}
	}
	return 0
}

func targetCutIndex(messages []sdk.MessageParam, targetTokens int) int {
	if len(messages) == 0 || targetTokens <= 0 {
		return 0
	}
	retainedStart := len(messages)
	for retainedStart > 0 {
		candidate := retainedStart - 1
		if ApproxTokensFromMessages(messages[candidate:]) > targetTokens && retainedStart < len(messages) {
			break
		}
		retainedStart = candidate
	}
	if retainedStart <= 0 || retainedStart >= len(messages) {
		return 0
	}
	boundary := previousTurnBoundary(messages, retainedStart+1)
	if boundary <= 0 {
		return retainedStart
	}
	return boundary
}

func compactionMessages(summary string, messages []sdk.MessageParam) []sdk.MessageParam {
	working := append([]sdk.MessageParam(nil), messages...)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return working
	}
	legacySummary := sdk.NewUserMessage(sdk.NewTextBlock("Previously compacted session context:\n" + summary))
	return append([]sdk.MessageParam{legacySummary}, working...)
}

func compressMessagesWithHeadroom(messages []sdk.MessageParam, tokenBudget int) ([]sdk.MessageParam, error) {
	blockBudget := perContentBlockBudget(messages, tokenBudget)
	compressed := make([]sdk.MessageParam, 0, len(messages))
	for _, message := range messages {
		blocks := make([]sdk.ContentBlockParamUnion, 0, len(message.Content))
		for _, block := range message.Content {
			compressedBlock, err := compressContentBlockWithHeadroom(block, blockBudget, string(message.Role))
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, compressedBlock)
		}
		if len(blocks) == 0 {
			continue
		}
		compressed = append(compressed, sdk.MessageParam{Role: message.Role, Content: blocks})
	}
	return compressed, nil
}

func perContentBlockBudget(messages []sdk.MessageParam, tokenBudget int) int {
	if tokenBudget <= 0 {
		return 0
	}
	compressible := 0
	for _, message := range messages {
		for _, block := range message.Content {
			if block.OfText != nil || block.OfToolResult != nil {
				compressible++
			}
		}
	}
	if compressible <= 0 {
		return tokenBudget
	}
	budget := tokenBudget / compressible
	if budget < 128 {
		budget = 128
	}
	return budget
}

func compressContentBlockWithHeadroom(block sdk.ContentBlockParamUnion, tokenBudget int, role string) (sdk.ContentBlockParamUnion, error) {
	if block.OfText != nil {
		textBlock := *block.OfText
		compressed, err := compressTextWithHeadroom(textBlock.Text, tokenBudget, role)
		if err != nil {
			return block, err
		}
		textBlock.Text = compressed
		block.OfText = &textBlock
		return block, nil
	}
	if block.OfToolResult != nil {
		toolResult := *block.OfToolResult
		content := make([]sdk.ToolResultBlockParamContentUnion, 0, len(toolResult.Content))
		for _, item := range toolResult.Content {
			if item.OfText == nil {
				content = append(content, item)
				continue
			}
			textBlock := *item.OfText
			compressed, err := compressTextWithHeadroom(textBlock.Text, tokenBudget, "tool")
			if err != nil {
				return block, err
			}
			textBlock.Text = compressed
			item.OfText = &textBlock
			content = append(content, item)
		}
		toolResult.Content = content
		block.OfToolResult = &toolResult
		return block, nil
	}
	return block, nil
}

func compressTextWithHeadroom(text string, tokenBudget int, role string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return text, nil
	}
	opts := headroom.DefaultOptions()
	opts.Aggressiveness = 1.0
	opts.Reversible = false
	opts.EnablePipeline = true
	opts.TokenBudget = tokenBudget
	result, err := headroom.Compress([]headroom.Message{{Role: headroomRole(role), Content: text}}, opts)
	if err != nil {
		return "", err
	}
	if result == nil || len(result.Messages) == 0 || strings.TrimSpace(result.Messages[0].Content) == "" {
		return text, nil
	}
	return strings.TrimSpace(result.Messages[0].Content), nil
}

func headroomRole(role string) string {
	switch strings.TrimSpace(role) {
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	case "tool":
		return "tool"
	default:
		return "user"
	}
}

func sessionTranscript(messages []sdk.MessageParam) string {
	return strings.TrimSpace(Transcript(messages))
}

func messageText(message sdk.MessageParam) string {
	parts := make([]string, 0, len(message.Content))
	for _, block := range message.Content {
		text := strings.TrimSpace(contentBlockText(block))
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func contentBlockText(block sdk.ContentBlockParamUnion) string {
	if block.OfText != nil {
		return block.OfText.Text
	}
	if block.OfToolResult != nil {
		return toolResultText(block.OfToolResult)
	}
	if block.OfToolUse != nil {
		return toolUseText(block.OfToolUse)
	}
	return ""
}

func toolUseText(block *sdk.ToolUseBlockParam) string {
	if block == nil {
		return ""
	}
	input := strings.TrimSpace(formatJSON(block.Input))
	if input == "" {
		return "[tool use: " + block.Name + "]"
	}
	return "[tool use: " + block.Name + "]\n" + input
}

func toolResultText(block *sdk.ToolResultBlockParam) string {
	if block == nil {
		return ""
	}
	parts := make([]string, 0, len(block.Content))
	for _, content := range block.Content {
		if content.OfText != nil {
			parts = append(parts, content.OfText.Text)
			continue
		}
		if text := content.GetText(); text != nil {
			parts = append(parts, *text)
			continue
		}
		if raw := strings.TrimSpace(formatJSON(content)); raw != "" {
			parts = append(parts, raw)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return "[tool result]"
	}
	return "[tool result]\n" + text
}

func formatJSON(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}
