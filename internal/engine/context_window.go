package engine

import (
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/contextcompression"
	contextmessage "github.com/solosw/solcode/internal/message"
)

// recentContextWindow changes the active view only. Old source messages stay in
// the local archive; no model or summary is needed to recover from a hard limit.
func recentContextWindow(messages []sdk.MessageParam, builder ContextBuilder, req BuildRequest, limit int64, rejected bool) ([]sdk.MessageParam, error) {
	req.Messages = messages
	before := builder.EstimateContextTokens(req)
	target := limit * 70 / 100
	if limit <= 0 || (rejected && before*70/100 < target) {
		target = before * 70 / 100
	}
	// Reserve the configured reply allowance as well as request estimation slack.
	output := req.MaxTokens
	if output <= 0 {
		output = 16000
	}
	if limit > 0 && limit-output-1024 < target {
		target = limit - output - 1024
	}
	if target <= 0 {
		return nil, fmt.Errorf("context budget cannot fit the system/tools and output allowance")
	}
	latestUser := -1
	for i, m := range messages {
		if realWindowUser(m) {
			latestUser = i
		}
	}
	// Only cut where every earlier tool call has a result. A parallel tool group
	// must never be split, even when result messages are interleaved with overlays.
	pending := map[string]bool{}
	cuts := make([]int, 0, len(messages))
	for i, m := range messages {
		if i > 0 && len(pending) == 0 && (string(m.Role) == "user" || latestUser < i) {
			cuts = append(cuts, i)
		}
		for _, b := range m.Content {
			if b.OfToolUse != nil {
				pending[b.OfToolUse.ID] = true
			}
			if b.OfToolResult != nil {
				delete(pending, b.OfToolResult.ToolUseID)
			}
		}
	}
	candidate := func(cut int) []sdk.MessageParam {
		out := make([]sdk.MessageParam, 0, len(messages)-cut+1)
		// Preserve the current request even if a long sequence of completed tool
		// exchanges from that same turn has to fall outside the new window.
		if latestUser >= 0 && latestUser < cut {
			out = append(out, messages[latestUser])
		}
		out = append(out, messages[cut:]...)
		return out
	}
	// Suffix sizes decrease monotonically (the same user request stays anchored).
	low, high := 0, len(cuts)
	for low < high {
		mid := (low + high) / 2
		req.Messages = candidate(cuts[mid])
		if builder.EstimateContextTokens(req) <= target {
			high = mid
		} else {
			low = mid + 1
		}
	}
	if low == len(cuts) {
		return nil, fmt.Errorf("latest request or complete tool exchange cannot fit the context budget after trimming older messages")
	}
	out := candidate(cuts[low])
	req.Messages = out
	if len(out) >= len(messages) || builder.EstimateContextTokens(req) >= before {
		return nil, fmt.Errorf("no older complete messages can be removed from the context window")
	}
	return out, nil
}

func realWindowUser(m sdk.MessageParam) bool {
	if string(m.Role) != "user" || contextmessage.IsDynamicContext(m) || contextmessage.IsBackgroundToolNotification(m) {
		return false
	}
	for _, b := range m.Content {
		if b.OfToolResult != nil {
			return false
		}
		if b.OfText != nil && (strings.HasPrefix(b.OfText.Text, contextcompression.Header) || strings.HasPrefix(b.OfText.Text, "Compacted session summary:") || strings.HasPrefix(b.OfText.Text, "Compacted project knowledge:")) {
			return false
		}
	}
	return true
}

func contextLimitError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"context_length_exceeded", "context_window_exceeded", "maximum context length", "exceeds the context window", "exceeds the model's context", "prompt is too long", "prompt too long", "input is too long", "too many input tokens", "context length exceeded", "not enough context headroom"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
