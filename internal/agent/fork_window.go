package agent

import (
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/tokenest"
)

// windowFork defaults to full inheritance. Large forks retain a recent suffix
// within 40% of the child capacity. Tool calls and results form indivisible
// groups; no structured tool input or result is cut at an arbitrary byte.
func windowFork(parent Snapshot, capacity int64) Snapshot {
	if capacity <= 0 {
		return parent
	}
	workingTokens := tokenest.Text(string(parent.WorkingSet))
	if int64(tokenest.Messages(parent.Messages)+workingTokens) <= capacity*4/5 {
		return parent
	}
	budget := int(capacity * 2 / 5)
	out := Snapshot{WorkingSet: parent.WorkingSet}
	// Working-set JSON is metadata, never truncate it into invalid JSON.
	if workingTokens > budget {
		out.WorkingSet = nil
		workingTokens = 0
	}
	budget -= workingTokens
	start := len(parent.Messages)
	used := 0
	for start > 0 {
		previous := start - 1
		for _, block := range parent.Messages[previous].Content {
			if block.OfToolResult != nil && previous > 0 {
				previous--
				break
			}
		}
		cost := tokenest.Messages(parent.Messages[previous:start])
		if used+cost > budget {
			break
		}
		used += cost
		start = previous
	}
	out.Messages = parent.Messages[start:]
	// Tokenization of concatenated messages can differ slightly from the sum
	// of individual groups. Verify the actual suffix before accepting it.
	for len(out.Messages) > 0 && tokenest.Messages(out.Messages) > budget {
		end := 1
		for _, block := range out.Messages[0].Content {
			if block.OfToolUse != nil && len(out.Messages) > 1 {
				end = 2
				break
			}
		}
		out.Messages = out.Messages[end:]
	}
	// A single large plain-text message can still supply recent context. Keep
	// its tail on rune boundaries; never apply this to tool/image/thinking blocks.
	if len(out.Messages) == 0 && len(parent.Messages) > 0 && budget > 0 {
		if message, ok := tailTextMessage(parent.Messages[len(parent.Messages)-1], budget); ok {
			out.Messages = []sdk.MessageParam{message}
		}
	}
	return out
}

func tailTextMessage(message sdk.MessageParam, budget int) (sdk.MessageParam, bool) {
	var parts []string
	for _, block := range message.Content {
		if block.OfText == nil {
			return sdk.MessageParam{}, false
		}
		parts = append(parts, block.OfText.Text)
	}
	text := []rune(strings.Join(parts, "\n"))
	const marker = "[Earlier parent context omitted by inheritance window.]\n"
	low, high := 0, len(text)
	var best sdk.MessageParam
	found := false
	for low <= high {
		n := low + (high-low)/2
		candidate := message
		candidate.Content = []sdk.ContentBlockParamUnion{sdk.NewTextBlock(marker + string(text[len(text)-n:]))}
		if tokenest.Messages([]sdk.MessageParam{candidate}) <= budget {
			best, found = candidate, true
			low = n + 1
		} else {
			high = n - 1
		}
	}
	return best, found
}
