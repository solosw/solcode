package unit_tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/memory"
)

func TestMemoryFileStoreRememberDeduplicates(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())

	first, created, err := store.Remember(ctx, "I prefer concise final answers", "main")
	if err != nil {
		t.Fatalf("remember first: %v", err)
	}
	if !created {
		t.Fatal("expected first memory to be created")
	}
	second, created, err := store.Remember(ctx, "  I prefer concise final answers  ", "other")
	if err != nil {
		t.Fatalf("remember duplicate: %v", err)
	}
	if created {
		t.Fatal("expected duplicate memory to update existing item")
	}
	if first.ID != second.ID {
		t.Fatalf("expected duplicate id %q, got %q", first.ID, second.ID)
	}
	if second.AccessCount <= first.AccessCount {
		t.Fatalf("expected access count to increase, before=%d after=%d", first.AccessCount, second.AccessCount)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(items))
	}
}

func TestKeywordRetrieverRanksMatchingMemory(t *testing.T) {
	items := []memory.Item{
		memory.NewItem("Use verbose explanations for architecture reviews", memory.TierLongTerm, "s1"),
		memory.NewItem("Prefer concise final answers", memory.TierLongTerm, "s1"),
	}
	items[1].Importance = 0.9
	got := memory.KeywordRetriever{Items: items}.Retrieve("please keep final answers concise", 1)
	if len(got) != 1 {
		t.Fatalf("expected 1 retrieved memory, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "concise") {
		t.Fatalf("expected concise memory, got %q", got[0].Text)
	}
}

func TestExplicitMemoryFromPromptFiltersSecrets(t *testing.T) {
	text, ok := memory.ExplicitMemoryFromPrompt("请记住: 我喜欢中文回复")
	if !ok || text != "我喜欢中文回复" {
		t.Fatalf("expected Chinese memory extraction, got ok=%v text=%q", ok, text)
	}
	if text, ok := memory.ExplicitMemoryFromPrompt("remember my api_key is sk-abcdefghijklmnopqrstuvwxyz123456"); ok || text != "" {
		t.Fatalf("expected sensitive memory to be filtered, got ok=%v text=%q", ok, text)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestMemoryFileStoreUsesJSONFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := memory.NewFileStore(dir)
	item, _, err := store.Remember(ctx, "persist this memory", "main")
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("unexpected items: %#v", items)
	}
	path := filepath.Join(dir, item.ID+".json")
	if !fileExists(path) {
		t.Fatalf("expected memory file %s", path)
	}
}

func TestMemoryItemSourceSessionIDPersists(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())
	_, _, err := store.Remember(ctx, "shared cross-session fact", "session-A")
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].SourceSessionID != "session-A" {
		t.Fatalf("expected SourceSessionID session-A, got %q", items[0].SourceSessionID)
	}
}
