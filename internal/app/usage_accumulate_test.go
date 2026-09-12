package app

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/session"
)

func TestEmitUsageAccumulatesTaskAndCompactionBilling(t *testing.T) {
	var forwarded []engine.Usage
	a := &App{
		Config: config.Config{MaxContextTokens: 200_000},
		onUsage: func(u engine.Usage) {
			forwarded = append(forwarded, u)
		},
	}

	s := session.NewSession(session.SessionID("main"), t.TempDir(), "model")
	unbind := a.bindUsageSession(s)
	defer unbind()

	// Main turn with occupancy.
	a.emitUsage(engine.Usage{
		EstimatedContextTokens:   12_000,
		InputTokens:              100,
		OutputTokens:             10,
		CacheCreationInputTokens: 20,
		CacheReadInputTokens:     30,
		MaxContextTokens:         200_000,
	})
	// Task turn: billing only.
	a.emitUsage(engine.Usage{
		InputTokens:              40,
		OutputTokens:             5,
		CacheCreationInputTokens: 6,
		CacheReadInputTokens:     7,
		MaxContextTokens:         200_000,
	})
	// Compaction side-channel.
	a.reportAPIUsage(&sdk.Message{Usage: sdk.Usage{
		InputTokens:              8,
		OutputTokens:             2,
		CacheCreationInputTokens: 1,
		CacheReadInputTokens:     3,
	}})

	usage := s.Metadata.Usage
	if usage.InputTokens != 148 || usage.OutputTokens != 17 || usage.CacheCreationInputTokens != 27 || usage.CacheReadInputTokens != 40 {
		t.Fatalf("session usage = %+v", usage)
	}
	if len(forwarded) != 3 {
		t.Fatalf("forwarded = %d, want 3", len(forwarded))
	}
	if forwarded[0].EstimatedContextTokens != 12_000 {
		t.Fatalf("first occupancy = %d", forwarded[0].EstimatedContextTokens)
	}
	if forwarded[1].EstimatedContextTokens != 0 || forwarded[2].EstimatedContextTokens != 0 {
		t.Fatalf("task/compact occupancy should stay 0: %#v %#v", forwarded[1], forwarded[2])
	}
	last := forwarded[2]
	if last.InputTokens != 148 || last.CacheReadInputTokens != 40 {
		t.Fatalf("forwarded absolutes = %+v", last)
	}
}
