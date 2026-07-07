package app

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/session"
)

func TestPreTurnCompactionBehavior(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.Enabled = true
	cfg.Memory.CompactionTriggerPercent = 85
	cfg.Memory.CompactionTargetPercent = 50
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	for session.ApproxTokensFromMessages(current.CopyMessages()) < 85 {
		current.Append(
			sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("u", 40))),
			sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("a", 40))),
		)
	}
	before := session.ApproxTokensFromMessages(current.CopyMessages())
	if !application.shouldCompact(context.Background(), current) {
		t.Fatalf("expected session over 85%% to be marked for next-turn compaction, got %d tokens", before)
	}
	// Simulate end of current turn: nothing should be compacted yet.
	afterCurrentTurn := session.ApproxTokensFromMessages(current.CopyMessages())
	if afterCurrentTurn != before {
		t.Fatalf("expected no same-turn compaction, before=%d after=%d", before, afterCurrentTurn)
	}
}
