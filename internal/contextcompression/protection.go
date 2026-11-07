package contextcompression

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	contextmessage "github.com/solosw/solcode/internal/message"
	"strings"
)

// Keep the latest runtime overlays verbatim. Otherwise folding an old mode or
// role overlay could hide the still-current policy while the host deduplicates it.
func protectedIndexes(messages []sdk.MessageParam) map[int]bool {
	keep := map[int]bool{}
	latestUser := -1
	overlays := map[string]int{}
	for i, m := range messages {
		if protected(m) {
			latestUser = i
		}
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
	if latestUser >= 0 {
		keep[latestUser] = true
	}
	for _, i := range overlays {
		keep[i] = true
	}
	for i := len(messages) - 2; i < len(messages); i++ {
		if i >= 0 {
			keep[i] = true
		}
	}
	return keep
}
