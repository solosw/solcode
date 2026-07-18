package tui

import (
	"strings"
	"testing"
)

func TestTokenUsageAccumulatesSessionTotals(t *testing.T) {
	m := New(nil)
	m.tokenUsage.MaxContextTokens = 200_000

	// Per-request deltas accumulate when SessionTotals is false.
	updated, _ := m.Update(TokenUsageMsg{
		EstimatedContextTokens:   10_000,
		InputTokens:              1_000,
		OutputTokens:             100,
		CacheCreationInputTokens: 500,
		CacheReadInputTokens:     2_000,
		MaxContextTokens:         200_000,
	})
	m = updated.(Model)

	updated, _ = m.Update(TokenUsageMsg{
		EstimatedContextTokens:   12_000,
		InputTokens:              800,
		OutputTokens:             50,
		CacheCreationInputTokens: 0,
		CacheReadInputTokens:     3_000,
		MaxContextTokens:         200_000,
	})
	m = updated.(Model)

	if m.tokenUsage.EstimatedContextTokens != 12_000 {
		t.Fatalf("EstimatedContextTokens = %d, want latest 12000", m.tokenUsage.EstimatedContextTokens)
	}
	if m.tokenUsage.InputTokens != 1_800 {
		t.Fatalf("InputTokens session total = %d, want 1800", m.tokenUsage.InputTokens)
	}
	if m.tokenUsage.OutputTokens != 150 {
		t.Fatalf("OutputTokens session total = %d, want 150", m.tokenUsage.OutputTokens)
	}
	if m.tokenUsage.CacheCreationInputTokens != 500 {
		t.Fatalf("CacheCreation session total = %d, want 500", m.tokenUsage.CacheCreationInputTokens)
	}
	if m.tokenUsage.CacheReadInputTokens != 5_000 {
		t.Fatalf("CacheRead session total = %d, want 5000", m.tokenUsage.CacheReadInputTokens)
	}

	status := m.renderUsageStatus()
	if !strings.Contains(status, "[") || !strings.Contains(status, "]") {
		t.Fatalf("status should include progress bar, got %q", status)
	}
	if !strings.Contains(status, "cache [") {
		t.Fatalf("status should show combined cache bar, got %q", status)
	}
	// 5k read + 500 write = 5.5k combined
	if !strings.Contains(status, "5.5k") && !strings.Contains(status, "5500") {
		t.Fatalf("status should show combined cache total 5.5k, got %q", status)
	}
}

func TestUsageStatusAlwaysShowsCache(t *testing.T) {
	m := New(nil)
	m.tokenUsage.MaxContextTokens = 200_000
	// Fresh session: zeros should still render separate read/write bars.
	status := m.renderUsageStatus()
	if !strings.Contains(status, "cache [░░░░░░░░] 0 (0%)") {
		t.Fatalf("expected always-visible zero cache bar, got %q", status)
	}

	// input=1000, read=800, write=200 → input-side total=2000
	// read share 40%, write share 10%
	m.ApplyTokenUsage(TokenUsageMsg{
		EstimatedContextTokens:   10_000,
		InputTokens:              1_000,
		OutputTokens:             50,
		CacheCreationInputTokens: 200,
		CacheReadInputTokens:     800,
		MaxContextTokens:         200_000,
		SessionTotals:            true,
	})
	status = m.renderUsageStatus()
	// input=1000, read=800, write=200 → cache=1000, input-side=2000 → 50%
	if !strings.Contains(status, "cache [") || !strings.Contains(status, "(50%)") {
		t.Fatalf("expected combined cache 50%% share, got %q", status)
	}
	if !strings.Contains(status, "1k") && !strings.Contains(status, "1000") {
		if !strings.Contains(status, "1k") && !strings.Contains(status, "1000") {
			t.Fatalf("expected combined cache total 1k, got %q", status)
		}
	}
}

func TestTokenSharePercent(t *testing.T) {
	if got := tokenSharePercent(0, 0); got != "0%" {
		t.Fatalf("empty = %q", got)
	}
	if got := tokenSharePercent(1, 4); got != "25%" {
		t.Fatalf("1/4 = %q", got)
	}
	if got := tokenSharePercent(5, 4); got != "100%" {
		// clamp
		t.Fatalf("over = %q", got)
	}
}

func TestTokenUsageSessionTotalsReplace(t *testing.T) {
	m := New(nil)
	// Seed with deltas.
	updated, _ := m.Update(TokenUsageMsg{
		InputTokens:          100,
		CacheReadInputTokens: 500,
		MaxContextTokens:     100_000,
	})
	m = updated.(Model)

	// Absolute session totals from persisted session replace counters.
	updated, _ = m.Update(TokenUsageMsg{
		EstimatedContextTokens:   20_000,
		InputTokens:              9_000,
		OutputTokens:             400,
		CacheCreationInputTokens: 1_000,
		CacheReadInputTokens:     8_000,
		MaxContextTokens:         200_000,
		SessionTotals:            true,
	})
	m = updated.(Model)

	if m.tokenUsage.InputTokens != 9_000 {
		t.Fatalf("InputTokens = %d, want absolute 9000", m.tokenUsage.InputTokens)
	}
	if m.tokenUsage.CacheReadInputTokens != 8_000 {
		t.Fatalf("CacheRead = %d, want absolute 8000", m.tokenUsage.CacheReadInputTokens)
	}
	if m.tokenUsage.OutputTokens != 400 {
		t.Fatalf("OutputTokens = %d, want 400", m.tokenUsage.OutputTokens)
	}
	if m.tokenUsage.EstimatedContextTokens != 20_000 {
		t.Fatalf("EstimatedContextTokens = %d, want 20000", m.tokenUsage.EstimatedContextTokens)
	}
}

func TestTokenUsageResetsOnSessionReplace(t *testing.T) {
	m := New(nil)
	updated, _ := m.Update(TokenUsageMsg{
		InputTokens:          100,
		OutputTokens:         20,
		CacheReadInputTokens: 1_000,
		MaxContextTokens:     100_000,
	})
	m = updated.(Model)
	if m.tokenUsage.CacheReadInputTokens != 1_000 {
		t.Fatalf("precondition failed: cache read = %d", m.tokenUsage.CacheReadInputTokens)
	}

	updated, _ = m.Update(ReplaceMessagesMsg{Messages: []ChatMessage{m.systemMessage("loaded")}})
	m = updated.(Model)
	if m.tokenUsage.CacheReadInputTokens != 0 || m.tokenUsage.InputTokens != 0 || m.tokenUsage.OutputTokens != 0 {
		t.Fatalf("expected totals reset after session replace, got %+v", m.tokenUsage)
	}
	if m.tokenUsage.MaxContextTokens != 100_000 {
		t.Fatalf("MaxContextTokens should be preserved, got %d", m.tokenUsage.MaxContextTokens)
	}
}

func TestRenderContextProgressBar(t *testing.T) {
	empty := renderContextProgressBar(0, 100, 10)
	if empty != "[░░░░░░░░░░]" {
		t.Fatalf("empty bar = %q", empty)
	}
	full := renderContextProgressBar(100, 100, 10)
	if full != "[██████████]" {
		t.Fatalf("full bar = %q", full)
	}
	half := renderContextProgressBar(50, 100, 10)
	if !strings.Contains(half, "█") || !strings.Contains(half, "░") {
		t.Fatalf("half bar should mix filled/empty, got %q", half)
	}
	// No limit → empty track.
	unknown := renderContextProgressBar(10, 0, 8)
	if unknown != "[░░░░░░░░]" {
		t.Fatalf("unknown limit bar = %q", unknown)
	}
}

func TestSelectResultRestoresTokenUsage(t *testing.T) {
	m := New(nil)
	m.tokenUsage.CacheReadInputTokens = 999
	m.applySelectResult(SelectResult{
		ReplaceMessages: true,
		Messages:        []ChatMessage{m.systemMessage("switched")},
		TokenUsage: &TokenUsageMsg{
			InputTokens:          1_500,
			CacheReadInputTokens: 7_000,
			MaxContextTokens:     200_000,
			SessionTotals:        true,
		},
	})
	if m.tokenUsage.CacheReadInputTokens != 7_000 {
		t.Fatalf("CacheRead after select = %d, want 7000", m.tokenUsage.CacheReadInputTokens)
	}
	if m.tokenUsage.InputTokens != 1_500 {
		t.Fatalf("InputTokens after select = %d, want 1500", m.tokenUsage.InputTokens)
	}
}
