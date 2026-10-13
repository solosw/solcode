package message

import (
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

const dynamicContextPrefix = `<solcode-context kind="`
const dynamicContextClose = "\n</solcode-context>"

func DynamicContextText(kind, body string) string {
	return fmt.Sprintf("%s%s\">\n%s%s", dynamicContextPrefix, kind, body, dynamicContextClose)
}

func ParseDynamicContextText(text string) (kind, body string, ok bool) {
	if !strings.HasPrefix(text, dynamicContextPrefix) || !strings.HasSuffix(text, dynamicContextClose) {
		return "", "", false
	}
	kind, body, ok = strings.Cut(strings.TrimPrefix(text, dynamicContextPrefix), "\">\n")
	if !ok {
		return "", "", false
	}
	switch kind {
	case "mode", "skill", "project-knowledge", "session-summary", "memory", "folded-tools", "working-set", "efficiency":
		return kind, strings.TrimSuffix(body, dynamicContextClose), true
	default:
		return "", "", false
	}
}

// IsDynamicContext identifies pure engine overlay messages, never messages
// mixed with real user text, attachments, or tool results. They are durable
// model context but not user turns or user-visible conversation entries.
func IsDynamicContext(msg sdk.MessageParam) bool {
	if string(msg.Role) != "user" || len(msg.Content) == 0 {
		return false
	}
	for _, block := range msg.Content {
		if block.OfText == nil {
			return false
		}
		if _, _, ok := ParseDynamicContextText(block.OfText.Text); !ok {
			return false
		}
	}
	return true
}

// WithoutDynamicContext is for presentation and user-intent/archival inputs.
// Do not use it for model requests, storage, or context-token estimation.
func WithoutDynamicContext(messages []sdk.MessageParam) []sdk.MessageParam {
	out := make([]sdk.MessageParam, 0, len(messages))
	for _, msg := range messages {
		if !IsDynamicContext(msg) {
			out = append(out, msg)
		}
	}
	return out
}

// IsBackgroundToolNotification identifies the completion context of an already
// acknowledged background tool call. It is useful output, not a new user task.
func IsBackgroundToolNotification(msg sdk.MessageParam) bool {
	if string(msg.Role) != "user" || len(msg.Content) != 1 || msg.Content[0].OfText == nil {
		return false
	}
	text := strings.TrimSpace(msg.Content[0].OfText.Text)
	return strings.HasPrefix(text, "<background-task-notification>\n") && strings.HasSuffix(text, "\n</background-task-notification>")
}
