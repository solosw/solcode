package tokenest

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func Text(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return len([]rune(text))/4 + 1
}

func Messages(messages []sdk.MessageParam) int {
	return Text(Transcript(messages))
}

func Request(system string, messages []sdk.MessageParam, tools []sdk.ToolUnionParam) int {
	approx := Text(system)
	approx += Messages(messages)
	approx += Tools(tools)
	return approx
}

func Tools(tools []sdk.ToolUnionParam) int {
	approx := 0
	for _, toolDef := range tools {
		if toolDef.OfTool == nil {
			continue
		}
		approx += Text(toolDef.OfTool.Name)
		description := strings.TrimSpace(toolDef.OfTool.Description.Value)
		if description != "" {
			approx += Text(description)
		}
		approx += Text(fmt.Sprintf("%v", toolDef.OfTool.InputSchema))
	}
	return approx
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
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}
