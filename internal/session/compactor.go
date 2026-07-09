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
	messages = StripEphemeralContextMessages(messages)
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
		target = threshold * 15 / 100
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
		if messageStartsUserTurn(messages[i]) {
			userCount++
			if userCount >= keepTurns {
				return normalizeCutIndex(messages, i)
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
		if messageStartsUserTurn(messages[i]) {
			return normalizeCutIndex(messages, i)
		}
	}
	return 0
}

func messageStartsUserTurn(message sdk.MessageParam) bool {
	if string(message.Role) != "user" {
		return false
	}
	for _, block := range message.Content {
		if block.OfToolResult != nil {
			return false
		}
		if block.OfToolUse != nil {
			return false
		}
	}
	return true
}

func normalizeCutIndex(messages []sdk.MessageParam, idx int) int {
	if idx <= 0 || idx >= len(messages) {
		return idx
	}
	for idx > 0 {
		prev := messages[idx-1]
		hasAssistantToolUse := false
		for _, block := range prev.Content {
			if block.OfToolUse != nil {
				hasAssistantToolUse = true
				break
			}
		}
		if !hasAssistantToolUse {
			break
		}
		hasUserToolResult := true
		for _, block := range messages[idx].Content {
			if block.OfToolResult == nil {
				hasUserToolResult = false
				break
			}
		}
		if !hasUserToolResult {
			break
		}
		idx--
	}
	return idx
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

func compactionMessages(_ string, messages []sdk.MessageParam) []sdk.MessageParam {
	return StripEphemeralContextMessages(messages)
}

// StripEphemeralContextMessages removes transient context messages that are
// injected into model requests but must never become persisted conversation
// history. This also cleans older sessions that accidentally saved them.
func StripEphemeralContextMessages(messages []sdk.MessageParam) []sdk.MessageParam {
	out := make([]sdk.MessageParam, 0, len(messages))
	for _, message := range messages {
		if isEphemeralContextMessage(message) {
			continue
		}
		out = append(out, message)
	}
	return out
}

func isEphemeralContextMessage(message sdk.MessageParam) bool {
	if string(message.Role) != "user" || len(message.Content) == 0 {
		return false
	}
	text := strings.TrimSpace(messageText(message))
	lower := strings.ToLower(text)
	return strings.HasPrefix(lower, "session summary:\n") ||
		strings.HasPrefix(lower, "retrieved memory:\n") ||
		strings.HasPrefix(lower, "previously compacted session context:\n") ||
		(strings.Contains(lower, "\nretrieved memory:\n") && strings.Contains(lower, "session summary:"))
}

func compressMessagesWithHeadroom(messages []sdk.MessageParam, tokenBudget int) ([]sdk.MessageParam, error) {
	blockBudget := perContentBlockBudget(messages, tokenBudget)
	compressed := make([]sdk.MessageParam, 0, len(messages))
	toolNamesByID := map[string]string{}
	for _, message := range messages {
		blocks := make([]sdk.ContentBlockParamUnion, 0, len(message.Content))
		for _, block := range message.Content {
			if block.OfToolUse != nil {
				toolNamesByID[block.OfToolUse.ID] = block.OfToolUse.Name
			}
			compressedBlock, keepBlock, err := compressContentBlockWithHeadroom(block, blockBudget, string(message.Role), toolNamesByID)
			if err != nil {
				return nil, err
			}
			if !keepBlock {
				continue
			}
			if compressedBlock.OfToolUse != nil {
				toolNamesByID[compressedBlock.OfToolUse.ID] = compressedBlock.OfToolUse.Name
			}
			blocks = append(blocks, compressedBlock)
		}
		if len(blocks) == 0 {
			continue
		}
		compressed = append(compressed, sdk.MessageParam{Role: message.Role, Content: blocks})
	}
	return dropUnmatchedToolBlocks(compressed), nil
}

func dropUnmatchedToolBlocks(messages []sdk.MessageParam) []sdk.MessageParam {
	seenToolUses := map[string]bool{}
	seenToolResults := map[string]bool{}
	for _, message := range messages {
		for _, block := range message.Content {
			if block.OfToolUse != nil {
				id := strings.TrimSpace(block.OfToolUse.ID)
				if id != "" {
					seenToolUses[id] = true
				}
			}
			if block.OfToolResult != nil {
				id := strings.TrimSpace(block.OfToolResult.ToolUseID)
				if id != "" {
					seenToolResults[id] = true
				}
			}
		}
	}
	cleaned := make([]sdk.MessageParam, 0, len(messages))
	for _, message := range messages {
		blocks := make([]sdk.ContentBlockParamUnion, 0, len(message.Content))
		for _, block := range message.Content {
			if block.OfToolUse != nil {
				id := strings.TrimSpace(block.OfToolUse.ID)
				if id == "" || !seenToolResults[id] {
					continue
				}
			}
			if block.OfToolResult != nil {
				id := strings.TrimSpace(block.OfToolResult.ToolUseID)
				if id == "" || !seenToolUses[id] {
					continue
				}
			}
			blocks = append(blocks, block)
		}
		if len(blocks) == 0 {
			continue
		}
		cleaned = append(cleaned, sdk.MessageParam{Role: message.Role, Content: blocks})
	}
	return cleaned
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

func compressContentBlockWithHeadroom(block sdk.ContentBlockParamUnion, tokenBudget int, role string, toolNamesByID map[string]string) (sdk.ContentBlockParamUnion, bool, error) {
	if block.OfText != nil {
		textBlock := *block.OfText
		compressed, err := compressTextWithHeadroom(textBlock.Text, tokenBudget, role)
		if err != nil {
			return block, true, err
		}
		textBlock.Text = compressed
		block.OfText = &textBlock
		return block, true, nil
	}
	if block.OfToolUse != nil {
		toolUse := *block.OfToolUse
		if shouldPreserveToolUse(toolUse.Name) {
			return block, true, nil
		}
		if shouldDropTool(toolUse.Name) {
			return sdk.ContentBlockParamUnion{}, false, nil
		}
		toolUse.Input = map[string]any{"summary": "tool call preserved as summarized metadata", "name": toolUse.Name, "tool_id": toolUse.ID}
		block.OfToolUse = &toolUse
		return block, true, nil
	}
	if block.OfToolResult != nil {
		toolResult := *block.OfToolResult
		toolName := inferToolName(toolResult.ToolUseID, toolNamesByID)
		if shouldPreserveToolResult(toolName) {
			return block, true, nil
		}
		if shouldDropTool(toolName) {
			return sdk.ContentBlockParamUnion{}, false, nil
		}
		content := make([]sdk.ToolResultBlockParamContentUnion, 0, len(toolResult.Content))
		for _, item := range toolResult.Content {
			if item.OfText == nil {
				content = append(content, item)
				continue
			}
			textBlock := *item.OfText
			compressed, err := compressTextWithHeadroom(textBlock.Text, tokenBudget, "tool")
			if err != nil {
				return block, true, err
			}
			textBlock.Text = compressed
			item.OfText = &textBlock
			content = append(content, item)
		}
		if len(content) == 0 {
			content = []sdk.ToolResultBlockParamContentUnion{{OfText: &sdk.TextBlockParam{Text: "tool result preserved as summarized metadata for tool_id=" + toolResult.ToolUseID}}}
		}
		toolResult.Content = content
		block.OfToolResult = &toolResult
		return block, true, nil
	}
	return block, true, nil
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

func shouldDropTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "Bash":
		return true
	default:
		return false
	}
}

func shouldPreserveToolUse(name string) bool {
	switch strings.TrimSpace(name) {
	case "Edit", "Write", "Patch", "Diff":
		return true
	default:
		return false
	}
}

func shouldPreserveToolResult(name string) bool {
	return shouldPreserveToolUse(name)
}

func inferToolName(toolUseID string, toolNamesByID map[string]string) string {
	return strings.TrimSpace(toolNamesByID[toolUseID])
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
