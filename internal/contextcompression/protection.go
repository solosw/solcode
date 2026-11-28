package contextcompression

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	contextmessage "github.com/solosw/solcode/internal/message"
	"strings"
)

// Keep the latest assistant action and runtime overlays verbatim. User requests
// are compressible once they leave the active assistant/tool exchange.
func protectedIndexes(messages []sdk.MessageParam) map[int]bool {
	keep := map[int]bool{}
	overlays := map[string]int{}
	for i, m := range messages {
		for _, b := range m.Content {
			if b.OfText == nil {
				continue
			}
			if kind, _, ok := contextmessage.ParseDynamicContextText(b.OfText.Text); ok && kind != "context-budget" && kind != "efficiency" {
				overlays[kind] = i
			}
			if strings.HasPrefix(b.OfText.Text, "Compacted session summary:") || strings.HasPrefix(b.OfText.Text, "Compacted project knowledge:") {
				keep[i] = true
			}
		}
	}
	for _, i := range overlays {
		keep[i] = true
	}
	if len(messages) > 0 {
		keep[len(messages)-1] = true
	}
	return keep
}
