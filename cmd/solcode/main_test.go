package main

import (
	"context"
	"strings"
	"testing"

	appcore "github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

func TestLoadSanitizedSessionRewritesPollutedSummary(t *testing.T) {
	ctx := context.Background()
	store := session.NewFileStore(t.TempDir())
	manager := session.NewManager(store, "main")
	current := session.NewSession("main", t.TempDir(), "test-model")
	current.Summary = strings.Join([]string{
		"Session summary:",
		"user: 继续",
		"var b strings.Builder",
		"assistant: 我继续直接修，先把 **build/test 断点** 和 **旧 session summary 去污入口** 一起收掉。",
		"Compacted session file modifications: internal/anthropic/messages.go: edited; internal/app/app.go: edited; internal/app/app.go: edited; internal/engine/engine.go: edited; internal/engine/engine.go: edited.",
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"files := dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(priorHints, []string{\"files\", \"code sections\", \"file modifications\"})...))",
		"item.ID = strings.TrimSuffix(entry.Name(), \".json\")",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"@@ -810,7 +810,9 @@",
		"currentWork = []string{primary}",
		"internal/memory/sanitize.go",
		"internal/memory/memory.go",
		"internal/memory/manager.go",
		"internal/memory/sanitize_test.go",
		"internal/app/app.go",
		"unit_tests/memory_summary_test.go",
	}, "\n")
	if err := manager.Save(ctx, current); err != nil {
		t.Fatalf("save polluted session: %v", err)
	}

	application := &appcore.App{Sessions: manager}
	cfg := config.Default()
	cfg.WorkDir = current.Metadata.WorkDir
	cfg.Model = "test-model"

	loaded, err := loadSanitizedSession(ctx, application, "main", cfg)
	if err != nil {
		t.Fatalf("loadSanitizedSession: %v", err)
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
		if !strings.Contains(loaded.Summary, want) {
			t.Fatalf("expected sanitized loaded summary to contain %q, got:\n%s", want, loaded.Summary)
		}
	}
	for _, unwanted := range []string{
		"Session summary:",
		"user: 继续",
		"var b strings.Builder",
		"assistant: 我继续",
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"files := dedupeSummaryLines",
		"item.ID = strings.TrimSuffix",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"@@ -810,7 +810,9 @@",
		"currentWork = []string{primary}",
	} {
		if strings.Contains(loaded.Summary, unwanted) {
			t.Fatalf("did not expect %q in sanitized loaded summary:\n%s", unwanted, loaded.Summary)
		}
	}
	if strings.Contains(loaded.Summary, "internal/app/app.go: edited; internal/app/app.go: edited") {
		t.Fatalf("expected duplicated app.go entries to collapse, got:\n%s", loaded.Summary)
	}

	reloaded, err := manager.LoadOrCreate(ctx, "main", current.Metadata.WorkDir, "test-model")
	if err != nil {
		t.Fatalf("reload persisted session: %v", err)
	}
	if reloaded.Summary != loaded.Summary {
		t.Fatalf("expected sanitized summary to persist to disk\nloaded:\n%s\nreloaded:\n%s", loaded.Summary, reloaded.Summary)
	}
}
