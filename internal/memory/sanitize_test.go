package memory

import (
	"context"
	"testing"
)

func TestSanitizeStoredMemoryTextDropsToolUsageNoise(t *testing.T) {
	text := "Compacted session tools used: Edit, Bash."
	if got := sanitizeStoredMemoryText(text, []string{"tool-usage", "compaction"}); got != "" {
		t.Fatalf("expected tool-usage memory to be dropped, got %q", got)
	}
}

func TestSanitizeStoredMemoryTextCompactsVerboseModificationDetails(t *testing.T) {
	text := "Compacted session file modifications: internal/app/app.go: edited (replaced old behavior -> new behavior); internal/engine/engine.go: edited (replaced foo -> bar)."
	got := sanitizeStoredMemoryText(text, []string{"code-change", "files", "modifications"})
	for _, want := range []string{
		"Compacted session file modifications:",
		"internal/app/app.go: edited (targeted replacement)",
		"internal/engine/engine.go: edited (targeted replacement)",
	} {
		if !containsText(got, want) {
			t.Fatalf("expected %q in sanitized memory, got %q", want, got)
		}
	}
	for _, unwanted := range []string{"old behavior", "new behavior", "foo", "bar"} {
		if containsText(got, unwanted) {
			t.Fatalf("did not expect verbose detail %q in sanitized memory, got %q", unwanted, got)
		}
	}
}

func TestFileStoreListSelfHealsPollutedMemories(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	if _, err := store.Save(ctx, Item{
		ID:              "mods",
		Tier:            TierShortTerm,
		Kind:            KindTask,
		Scope:           ScopeProject,
		Text:            "Compacted session file modifications: internal/app/app.go: edited (replaced old behavior -> new behavior); internal/engine/engine.go: edited (replaced foo -> bar).",
		Tags:            []string{"code-change", "files", "modifications"},
		Importance:      0.8,
		Confidence:      0.8,
		RetentionScore:  0.8,
		AccessCount:     1,
		SourceSessionID: "s1",
	}); err != nil {
		t.Fatalf("save mods: %v", err)
	}
	if _, err := store.Save(ctx, Item{
		ID:              "tool",
		Tier:            TierWorking,
		Kind:            KindTask,
		Scope:           ScopeSession,
		Text:            "Compacted session tools used: Edit, Bash.",
		Tags:            []string{"tool-usage", "compaction"},
		Importance:      0.6,
		Confidence:      0.7,
		RetentionScore:  0.6,
		AccessCount:     1,
		SourceSessionID: "s1",
	}); err != nil {
		t.Fatalf("save tool: %v", err)
	}

	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected only sanitized modification memory to remain, got %#v", items)
	}
	if items[0].ID != "mods" {
		t.Fatalf("expected mods memory to remain, got %#v", items)
	}
	if !containsText(items[0].Text, "internal/app/app.go: edited (targeted replacement)") {
		t.Fatalf("expected sanitized modification text, got %q", items[0].Text)
	}
	if containsText(items[0].Text, "old behavior") || containsText(items[0].Text, "new behavior") {
		t.Fatalf("expected verbose replacement details removed, got %q", items[0].Text)
	}

	again, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List second pass: %v", err)
	}
	if len(again) != 1 || again[0].Text != items[0].Text {
		t.Fatalf("expected sanitized memory to persist after rewrite, got %#v", again)
	}
}

func containsText(text, want string) bool {
	return len(text) >= len(want) && (text == want || len(want) > 0 && contains(text, want))
}

func contains(text, want string) bool {
	return len(want) == 0 || (len(text) >= len(want) && stringIndex(text, want) >= 0)
}

func stringIndex(text, want string) int {
	for i := 0; i+len(want) <= len(text); i++ {
		if text[i:i+len(want)] == want {
			return i
		}
	}
	return -1
}
