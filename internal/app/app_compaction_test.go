package app

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/session"
)

func TestShouldCompactUses85PercentTrigger(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 85
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	for session.ApproxTokensFromMessages(current.CopyMessages()) < 85 {
		current.Append(
			sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("u", 40))),
			sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("a", 40))),
		)
	}
	if !application.shouldCompact(context.Background(), current) {
		t.Fatalf("expected compaction at or above 85%% threshold, got %d tokens", session.ApproxTokensFromMessages(current.CopyMessages()))
	}
}

func TestShouldCompactUsesEstimatedContextTokens(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 85
	cfg.SystemPrompt = strings.Repeat("system context ", 40)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("small prompt")))
	if session.ApproxTokensFromMessages(current.CopyMessages()) >= 85 {
		t.Fatal("test setup expected messages alone to stay below threshold")
	}
	if !application.shouldCompact(context.Background(), current) {
		t.Fatal("expected ctx estimate including system context to trigger compaction")
	}
}

func TestCompactUses50PercentTarget(t *testing.T) {
	messages := []sdk.MessageParam{}
	for i := 0; i < 12; i++ {
		messages = append(messages,
			sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("user turn ", 12))),
			sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("assistant turn ", 12))),
		)
	}
	before := session.ApproxTokensFromMessages(messages)
	result, err := session.Compact(context.Background(), "previous", messages, nil, session.CompactOptions{
		MaxRecentTurns:         20,
		SummaryThresholdTokens: 1,
		TargetTokens:           before / 2,
	})
	if err != nil {
		t.Fatalf("Compact() = %v", err)
	}
	if !result.Changed {
		t.Fatal("expected compaction to change session")
	}
	retained := session.ApproxTokensFromMessages(result.Messages)
	if retained >= before {
		t.Fatalf("expected compaction to reduce retained messages, before=%d after=%d", before, retained)
	}
	if retained > before*3/5 {
		t.Fatalf("expected retained messages to be pushed down toward the 50%% target, before=%d after=%d", before, retained)
	}
	if strings.TrimSpace(result.CompactedTranscript) == "" {
		t.Fatal("expected compacted transcript to be populated")
	}
}
