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

func TestMemorySummaryTriggerUses50PercentThreshold(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.SummaryTriggerPercent = 50
	cfg.SystemPrompt = strings.Repeat("system context ", 40)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("small prompt")))
	if !application.shouldRefreshMemorySummary(context.Background(), current) {
		t.Fatal("expected memory summary refresh to trigger at or above 50% threshold")
	}
}

func TestMemorySummaryTriggerRunsOncePerCycle(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.SummaryTriggerPercent = 50
	cfg.SystemPrompt = strings.Repeat("system context ", 40)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("small prompt")))

	if !application.shouldRefreshMemorySummary(context.Background(), current) {
		t.Fatal("expected first summary refresh to trigger")
	}
	current.Metadata.MemorySummaryCompleted = true
	if application.shouldRefreshMemorySummary(context.Background(), current) {
		t.Fatal("expected summary refresh to stay disabled after completing this threshold cycle")
	}
}

func TestMemoryMaintenanceCycleResetsAfterDroppingBelowSummaryThreshold(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 0
	cfg.Memory.SummaryThresholdTokens = 100000
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("memory ", 5000))))
	current.Metadata.MemorySummaryCompleted = true
	current.Metadata.MemoryCompactionCompleted = true

	application.resetMemoryMaintenanceCycleIfBelowThreshold(context.Background(), current)
	if !current.Metadata.MemorySummaryCompleted || !current.Metadata.MemoryCompactionCompleted {
		t.Fatal("expected cycle flags to remain set while session is still above threshold")
	}

	current.Summary = ""
	current.ReplaceMessages(nil)
	application.resetMemoryMaintenanceCycleIfBelowThreshold(context.Background(), current)
	if current.Metadata.MemorySummaryCompleted || current.Metadata.MemoryCompactionCompleted {
		t.Fatalf("expected cycle flags to reset after compaction drops below summary threshold, summary=%t compaction=%t", current.Metadata.MemorySummaryCompleted, current.Metadata.MemoryCompactionCompleted)
	}
}

func TestCompactTriggerRunsOncePerCycle(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 85
	cfg.SystemPrompt = strings.Repeat("system context ", 80)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("small prompt")))

	if !application.shouldCompact(context.Background(), current) {
		t.Fatal("expected first compaction to trigger")
	}
	current.Metadata.MemoryCompactionCompleted = true
	current.Metadata.MemoryCompactionMessageCount = len(current.Messages)
	if application.shouldCompact(context.Background(), current) {
		t.Fatal("expected compaction to stay disabled until new messages are added")
	}
	current.Append(sdk.NewAssistantMessage(sdk.NewTextBlock("new response after compaction")))
	if !application.shouldCompact(context.Background(), current) {
		t.Fatal("expected compaction to be allowed again after messages grow")
	}
}

func TestMemoryRetrievalBudgetCapsAtTenPercent(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 200_000
	application := &App{Config: cfg}
	if got := application.memoryRetrievalTokenBudget(); got != 20_000 {
		t.Fatalf("expected 20k retrieval budget for 200k context, got %d", got)
	}
	cfg.MaxContextTokens = 1_000_000
	application.Config = cfg
	if got := application.memoryRetrievalTokenBudget(); got != 50_000 {
		t.Fatalf("expected 50k retrieval budget cap for 1M context, got %d", got)
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

func TestSanitizeLoadedSessionStateRemovesPollutedSummary(t *testing.T) {
	current := session.NewSession("named", t.TempDir(), "test-model")
	current.Summary = strings.Join([]string{
		"1. Primary Request and Intent:",
		"- code-change, files · Compacted session file modifications: internal/app/app.go: edited (replaced old behavior -> new behavior); internal/engine/engine.go: edited (replaced foo -> bar).",
		"- tool-usage, compaction · Compacted session tool usage: Edit, Bash.",
		"340| current.ReplaceMessages(session.StripEphemeralContextMessages(current.CopyMessages()))",
		"```go",
		`{"old_string":"x","new_string":"y"}`,
	}, "\n")

	changed := sanitizeLoadedSessionState(current)
	if !changed {
		t.Fatal("expected polluted summary to be sanitized")
	}
	for _, unwanted := range []string{"replaced old behavior", "Compacted session tool usage", "340|", "```go", `"old_string"`, `"new_string"`} {
		if strings.Contains(current.Summary, unwanted) {
			t.Fatalf("did not expect %q in sanitized summary: %s", unwanted, current.Summary)
		}
	}
	for _, want := range []string{"Compacted session file modifications:", "internal/app/app.go: edited", "internal/engine/engine.go: edited"} {
		if !strings.Contains(current.Summary, want) {
			t.Fatalf("expected %q in sanitized summary: %s", want, current.Summary)
		}
	}
}

func TestSanitizeLoadedSessionStateRecompactsExactPollutedSessionSummarySample(t *testing.T) {
	current := session.NewSession("named", t.TempDir(), "test-model")
	current.Summary = strings.Join([]string{
		"Session summary:",
		"user: 继续",
		"var b strings.Builder",
		"+ \tvar b strings.Builder",
		"assistant: 我继续直接收尾：先把 `app.go` 的 build 错修掉，再把“加载旧 session 时自动去污 summary”接上，同时更新失效测试。",
		"assistant: 我继续直接修，先把 **build/test 断点** 和 **旧 session summary 去污入口** 一起收掉。",
		"for _, want := range []string{\"internal/app/app.go: edited\", \"old behavior\", \"new behavior\", \"go test ./internal/app ./internal/session\", \"Edit\", \"Bash\"} {",
		"+ \tfor _, want := range []string{\"internal/app/app.go: edited\", \"targeted replacement\", \"go test ./internal/app ./internal/session\", \"Edit\", \"Bash\"} {",
		"`{\"command\":\"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go\"}`",
		"Compacted session file modifications: internal/anthropic/messages.go: edited; internal/app/app.go: edited; internal/app/app.go: edited; internal/app/app.go: edited; internal/engine/engine.go: edited; internal/engine/engine.go: edited.",
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"Compacted session validation/build commands run: \" + strings.Join(cleaned, \"; \") + \".\".",
		"files := dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(priorHints, []string{\"files\", \"code sections\", \"file modifications\"})...))",
		"item.ID = strings.TrimSuffix(entry.Name(), \".json\")",
		"func (m *Manager) RememberExtracted(ctx context.Context, input ExtractionInput) ([]Item, error) {",
		"assistant: 这份 `Session summary` 说明还有一层摘要级噪声没截干净：",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"[→] Tighten prior summary sanitization drop diff/code/todo noise (in_progress)",
		"@@ -810,7 +810,9 @@",
		"pending := summarizePending(append(lines, priorHints...), compactPreviousSummary(previous))",
		"currentWork = []string{primary}",
		"internal/memory/sanitize.go",
		"internal/memory/memory.go",
		"internal/memory/manager.go",
		"internal/memory/sanitize_test.go",
		"internal/app/app.go",
		"unit_tests/memory_summary_test.go",
	}, "\n")

	changed := sanitizeLoadedSessionState(current)
	if !changed {
		t.Fatal("expected exact polluted summary sample to be sanitized")
	}
	for _, want := range []string{
		"Compacted session file modifications:",
		"internal/anthropic/messages.go: edited",
		"internal/app/app.go: edited",
		"internal/engine/engine.go: edited",
		"internal/memory/sanitize.go",
		"internal/memory/memory.go",
		"internal/memory/manager.go",
		"internal/memory/sanitize_test.go",
		"internal/app/app.go",
		"unit_tests/memory_summary_test.go",
	} {
		if !strings.Contains(current.Summary, want) {
			t.Fatalf("expected %q in sanitized summary: %s", want, current.Summary)
		}
	}
	for _, unwanted := range []string{
		"Session summary:",
		"user: 继续",
		"var b strings.Builder",
		"assistant: 我继续",
		`"command":"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go"`,
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"files := dedupeSummaryLines",
		"item.ID = strings.TrimSuffix",
		"func (m *Manager) RememberExtracted(ctx context.Context, input ExtractionInput) ([]Item, error) {",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"[→] Tighten prior summary sanitization drop diff/code/todo noise (in_progress)",
		"@@ -810,7 +810,9 @@",
		"currentWork = []string{primary}",
	} {
		if strings.Contains(current.Summary, unwanted) {
			t.Fatalf("did not expect %q in sanitized summary: %s", unwanted, current.Summary)
		}
	}
	if strings.Contains(current.Summary, "internal/app/app.go: edited; internal/app/app.go: edited") {
		t.Fatalf("expected duplicated app.go modification entries to collapse: %s", current.Summary)
	}
	if strings.Contains(current.Summary, "internal/engine/engine.go: edited; internal/engine/engine.go: edited") {
		t.Fatalf("expected duplicated engine.go modification entries to collapse: %s", current.Summary)
	}
}
