package engine

import (
	"context"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/session"
)

// RunSession resumes only model conversation. Historical tool calls are data;
// the normal loop can execute only tool calls from a new model response.
func (e *Engine) RunSession(ctx context.Context, req agent.SessionRequest) agent.AgentResult {
	e = e.forRun()
	if req.Config.Model == "" {
		req.Config.Model = e.config.ModelName
	}
	run := RunRequest{AgentConfig: req.Config, SessionID: string(req.Config.ID), Messages: req.Snapshot.Messages, WorkingSet: req.Snapshot.WorkingSet, Checkpoint: req.Save, SkipPrompt: req.Config.Attempt > 1 && len(req.Snapshot.Messages) > 0}
	// Never use the main session's app-bound compaction callback for a child.
	run.CompactMessages = func(ctx context.Context, messages []sdk.MessageParam) ([]sdk.MessageParam, error) {
		budget := int(e.config.MaxContextTokens / 2)
		if budget < 1 {
			budget = 30_000
		}
		var observations session.ObservationStore
		if e.config.SubagentSessionDir != "" {
			observations = session.NewFileObservationStore(session.ObservationStoreDir(e.config.SubagentSessionDir, session.SessionID(req.Config.ID)))
		}
		compacted, err := session.Compact(ctx, "", messages, nil, session.CompactOptions{Force: true, TargetTokens: budget, SummaryThresholdTokens: budget, MaxRecentTurns: 4, ObservationStore: observations, ObservationKeepTurns: 2})
		if err != nil {
			return nil, err
		}
		if !compacted.Changed || session.ApproxTokensFromMessages(compacted.Messages) >= session.ApproxTokensFromMessages(messages) {
			return nil, fmt.Errorf("subagent context cannot be reduced within budget; full history remains available via Task history")
		}
		repaired, _ := session.RepairMessages(compacted.Messages)
		return repaired, nil
	}
	result := e.RunWithHistory(ctx, run)
	// Tool/model errors may occur before the next normal checkpoint. Record the
	// last known transcript even when cancelled; never hide persistence failure.
	if result.Messages == nil {
		result.Messages = req.Snapshot.Messages
	}
	if req.Save != nil {
		if err := req.Save(agent.Checkpoint{Snapshot: agent.Snapshot{Messages: result.Messages}, Stage: "run_end"}); err != nil {
			result.AgentResult.Error = fmt.Sprintf("persist subagent history: %v", err)
			result.AgentResult.NoRetry = true
		}
	}
	// Retrying after any issued call could repeat a mutation. Only pre-tool model
	// failures are automatically retried; explicit follow-up remains possible.
	if result.AgentResult.Error != "" {
		if ctx.Err() != nil {
			result.AgentResult.NoRetry = true
		}
		for _, m := range result.Messages[lenPrefix(req.Snapshot.Messages, result.Messages):] {
			for _, b := range m.Content {
				if b.OfToolUse != nil {
					result.AgentResult.NoRetry = true
				}
			}
		}
	}
	return result.AgentResult
}

func legacyHistoryPrompt(messages []sdk.MessageParam) string { return session.Transcript(messages) }

func lenPrefix(before, after []sdk.MessageParam) int {
	n := len(before)
	if n > len(after) {
		return 0
	}
	return n
}

func (req RunRequest) saveCheckpoint(stage string, messages []sdk.MessageParam, w *WorkingSet, usage agent.TokenUsage, compacted bool) error {
	if req.Checkpoint == nil {
		return nil
	}
	cp := agent.Checkpoint{Snapshot: agent.Snapshot{Messages: messages}, Usage: usage, UsageKnown: true, Model: req.AgentConfig.Model, Stage: stage, Compacted: compacted}
	if w != nil {
		cp.WorkingSet = w.Snapshot()
	}
	if err := req.Checkpoint(cp); err != nil {
		return fmt.Errorf("persist subagent %s: %w", stage, err)
	}
	return nil
}
