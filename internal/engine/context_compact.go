package engine

import (
	"context"
	"fmt"
	"sort"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/contextcompression"
	"github.com/solosw/solcode/internal/session"
)

// compactContext stages organizer-authored folds using the same protection and
// archive rules as compress. The caller persists the result before committing it.
func (e *Engine) compactContext(ctx context.Context, state *contextcompression.State, messages []sdk.MessageParam, builder ContextBuilder, req BuildRequest) ([]sdk.MessageParam, *contextcompression.State, error) {
	if e.config.MaxContextTokens <= 0 {
		return messages, nil, nil
	}
	percent := e.config.CompactionTriggerPercent
	if percent <= 0 || percent > 100 {
		percent = 85
	}
	trigger := e.config.MaxContextTokens * int64(percent) / 100
	if builder.EstimateContextTokens(req) < trigger {
		return messages, nil, nil
	}
	next := state.Clone()
	if err := next.Sync(messages); err != nil {
		return messages, nil, err
	}
	ranges := next.Overview(int(e.config.MaxContextTokens), 0).CompressibleRanges
	// Reduce the largest completed section first to bound organizer requests.
	sort.SliceStable(ranges, func(i, j int) bool { return ranges[i].Tokens > ranges[j].Tokens })
	if len(ranges) == 0 {
		return messages, nil, nil
	}
	if e.config.OnStatus != nil {
		e.config.OnStatus("Compacting...")
		defer e.config.OnStatus("Thinking…")
	}
	for _, r := range ranges {
		a, b, err := next.Range(r.Start, r.End)
		if err != nil {
			return messages, nil, err
		}
		visible, err := next.Messages()
		if err != nil {
			return messages, nil, err
		}
		summary, err := e.config.ContextSummary(ctx, session.Transcript(visible[a:b]))
		if err != nil {
			return messages, nil, fmt.Errorf("automatic context compaction with memory.organizer: %w", err)
		}
		revision := next.Revision
		if _, err := next.Compress(contextcompression.Request{Start: r.Start, End: r.End, Summary: summary, Topic: "Automatic context summary", Revision: &revision}); err != nil {
			return messages, nil, fmt.Errorf("automatic context compaction: %w", err)
		}
		visible, err = next.Messages()
		if err != nil {
			return messages, nil, err
		}
		req.Messages = visible
		next.Nudge.Compressed(int(builder.EstimateContextTokens(req)))
		if builder.EstimateContextTokens(req) < trigger {
			return visible, next, nil
		}
	}
	visible, err := next.Messages()
	return visible, next, err
}
