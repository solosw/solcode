package tui

import (
	"fmt"
	"strings"
)

func renderMessages(messages []ChatMessage, t Theme, showTimestamp bool, width int) string {
	var b strings.Builder
	contentWidth := max(10, width-6)
	for _, msg := range messages {
		ts := renderTimestamp(msg, t, showTimestamp)
		switch msg.Role {
		case "user":
			renderUserMessage(&b, msg, t, ts, contentWidth)
		case "assistant":
			renderAssistantMessage(&b, msg, t, ts, contentWidth)
		case "error":
			renderErrorMessage(&b, msg, t, ts, contentWidth)
		case "tool":
			renderToolStartMessage(&b, msg, t, ts, contentWidth)
		case "tool-done":
			renderToolDoneMessage(&b, msg, t, ts, contentWidth)
		case "agent":
			renderAgentMessage(&b, msg, t, ts, contentWidth)
		default:
			renderSystemMessage(&b, msg, t, ts, contentWidth)
		}
	}
	return b.String()
}

func renderTimestamp(msg ChatMessage, t Theme, showTimestamp bool) string {
	if !showTimestamp || msg.TimeStamp.IsZero() {
		return ""
	}
	return " " + t.Dim.Render(msg.TimeStamp.Format("15:04"))
}

func renderUserMessage(b *strings.Builder, msg ChatMessage, t Theme, ts string, width int) {
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return
	}
	b.WriteString(t.User.Render(UserMark) + ts + "\n")
	b.WriteString(wrapIndent(content, width, ""))
	b.WriteString("\n")
}

func renderAssistantMessage(b *strings.Builder, msg ChatMessage, t Theme, ts string, width int) {
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return
	}
	b.WriteString(t.Assistant.Render(AssistantMark) + ts + "\n")
	markdown := renderMarkdown(content, t, max(20, width-len(Connector)))
	b.WriteString(indentBlock(markdown, t.Connector.Render(Connector)))
	b.WriteString("\n")
}

func renderErrorMessage(b *strings.Builder, msg ChatMessage, t Theme, ts string, width int) {
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return
	}
	b.WriteString(t.ErrorStyle.Render(ErrorMark) + ts + "\n")
	b.WriteString(wrapIndent(content, width, t.Connector.Render(Connector)))
	b.WriteString("\n")
}

func renderSystemMessage(b *strings.Builder, msg ChatMessage, t Theme, ts string, width int) {
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return
	}
	b.WriteString(t.System.Render(SystemMark) + ts + "\n")
	b.WriteString(wrapIndent(content, width, ""))
	b.WriteString("\n")
}

func renderToolStartMessage(b *strings.Builder, msg ChatMessage, t Theme, ts string, width int) {
	summary := toolInputSummary(msg.ToolName, msg.Content, width)
	title := AssistantMark + " " + msg.ToolName
	if summary != "" {
		title += " " + t.Dim.Render(summary)
	}
	b.WriteString(t.Tool.Render(title) + ts + "\n")
	body := "Running " + msg.ToolName + "…"
	if summary != "" {
		body += "\n" + summary
	}
	b.WriteString(wrapIndent(body, width, t.Connector.Render(Connector)))
	b.WriteString("\n")
}

func renderToolDoneMessage(b *strings.Builder, msg ChatMessage, t Theme, ts string, width int) {
	title := AssistantMark + " " + msg.ToolName
	if msg.IsError {
		title += " failed"
		b.WriteString(t.ErrorStyle.Render(title) + ts + "\n")
	} else {
		b.WriteString(t.ToolDone.Render(title) + ts + "\n")
	}
	out := strings.TrimSpace(msg.Content)
	if out == "" {
		out = "(no output)"
	}
	if msg.Collapsed {
		lines := strings.Split(out, "\n")
		if len(lines) > 1 {
			out = lines[0] + fmt.Sprintf("\n… %d more lines (Ctrl+O to expand)", len(lines)-1)
		}
	}
	b.WriteString(wrapIndent(out, width, t.Connector.Render(Connector)))
	b.WriteString("\n")
}

func renderAgentMessage(b *strings.Builder, msg ChatMessage, t Theme, ts string, width int) {
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return
	}
	title := AssistantMark + " Agent"
	if msg.ToolName != "" {
		title += " " + msg.ToolName
	}
	if msg.IsError {
		b.WriteString(t.ErrorStyle.Render(title) + ts + "\n")
	} else {
		b.WriteString(t.Tool.Render(title) + ts + "\n")
	}
	b.WriteString(wrapIndent(content, width, t.Connector.Render(Connector)))
	b.WriteString("\n")
}

func indentBlock(text, prefix string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// wrapIndent wraps text to width, prefixing continuation lines with contPrefix.
func wrapIndent(text string, width int, contPrefix string) string {
	if width < 4 {
		width = 4
	}
	var b strings.Builder
	lines := strings.Split(text, "\n")
	for lineIndex, line := range lines {
		if lineIndex == 0 {
			wrapped := wrapLine(line, width)
			for i, w := range wrapped {
				if i == 0 {
					b.WriteString(w + "\n")
				} else {
					b.WriteString(contPrefix + w + "\n")
				}
			}
		} else {
			wrapped := wrapLine(line, width-len(contPrefix))
			for _, w := range wrapped {
				b.WriteString(contPrefix + w + "\n")
			}
		}
	}
	return b.String()
}

func wrapLine(line string, width int) []string {
	if width < 1 {
		width = 1
	}
	if line == "" {
		return []string{""}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}
	cur := words[0]
	var out []string
	for _, word := range words[1:] {
		if len(cur)+1+len(word) <= width {
			cur += " " + word
		} else {
			out = append(out, cur)
			cur = word
		}
	}
	out = append(out, cur)
	return out
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
