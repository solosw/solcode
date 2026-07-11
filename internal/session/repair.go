package session

import (
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// RepairMessages removes incomplete tool-use exchanges from persisted history.
// Anthropic requires every tool_use block to be followed by matching tool_result
// blocks in the next user message. Keeping a partial exchange makes the entire
// session unusable on its next request.
func RepairMessages(messages []sdk.MessageParam) ([]sdk.MessageParam, int) {
	messages = StripEphemeralContextMessages(messages)
	out := make([]sdk.MessageParam, 0, len(messages))
	removed := 0

	for i := 0; i < len(messages); i++ {
		message := messages[i]
		if string(message.Role) != "assistant" {
			if hasToolResults(message) {
				cleaned, count := removeToolResults(message)
				removed += count
				if len(cleaned.Content) == 0 {
					continue
				}
				message = cleaned
			}
			out = append(out, message)
			continue
		}

		toolUseIDs := toolUseIDs(message)
		if len(toolUseIDs) == 0 {
			out = append(out, message)
			continue
		}

		if i+1 >= len(messages) || string(messages[i+1].Role) != "user" || !hasAllToolResults(messages[i+1], toolUseIDs) {
			cleaned, count := removeToolUses(message)
			removed += count
			if len(cleaned.Content) > 0 {
				out = append(out, cleaned)
			}
			continue
		}

		out = append(out, message)
		out = append(out, messages[i+1])
		i++
	}
	return out, removed
}

func toolUseIDs(message sdk.MessageParam) []string {
	ids := make([]string, 0)
	for _, block := range message.Content {
		if block.OfToolUse == nil {
			continue
		}
		id := strings.TrimSpace(block.OfToolUse.ID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func hasAllToolResults(message sdk.MessageParam, toolUseIDs []string) bool {
	results := make(map[string]bool, len(toolUseIDs))
	for _, block := range message.Content {
		if block.OfToolResult != nil {
			results[strings.TrimSpace(block.OfToolResult.ToolUseID)] = true
		}
	}
	for _, id := range toolUseIDs {
		if !results[id] {
			return false
		}
	}
	return true
}

func hasToolResults(message sdk.MessageParam) bool {
	for _, block := range message.Content {
		if block.OfToolResult != nil {
			return true
		}
	}
	return false
}

func removeToolUses(message sdk.MessageParam) (sdk.MessageParam, int) {
	blocks := make([]sdk.ContentBlockParamUnion, 0, len(message.Content))
	removed := 0
	for _, block := range message.Content {
		if block.OfToolUse != nil {
			removed++
			continue
		}
		blocks = append(blocks, block)
	}
	message.Content = blocks
	return message, removed
}

func removeToolResults(message sdk.MessageParam) (sdk.MessageParam, int) {
	blocks := make([]sdk.ContentBlockParamUnion, 0, len(message.Content))
	removed := 0
	for _, block := range message.Content {
		if block.OfToolResult != nil {
			removed++
			continue
		}
		blocks = append(blocks, block)
	}
	message.Content = blocks
	return message, removed
}
