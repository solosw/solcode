package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreCaptureDedupAndRestore(t *testing.T) {
	work := t.TempDir()
	sessionDir := t.TempDir()
	store, err := NewStore(sessionDir, "main", work, 10)
	if err != nil {
		t.Fatal(err)
	}

	turn0, err := store.BeginTurn("first edit")
	if err != nil || turn0 != 0 {
		t.Fatalf("BeginTurn = %d, %v", turn0, err)
	}
	original := "hello"
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	content := original
	if err := store.Capture("a.txt", &content); err != nil {
		t.Fatal(err)
	}
	// Second capture same path in same turn must be ignored.
	changed := "ignored"
	if err := store.Capture("a.txt", &changed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("mutated"), 0o644); err != nil {
		t.Fatal(err)
	}

	turn1, err := store.BeginTurn("create file")
	if err != nil || turn1 != 1 {
		t.Fatalf("BeginTurn = %d, %v", turn1, err)
	}
	if err := store.Capture("b.txt", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	cp, err := store.Load(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Files) != 1 || cp.Files[0].Content == nil || *cp.Files[0].Content != original {
		t.Fatalf("turn0 files = %#v", cp.Files)
	}

	result, err := store.RestoreFiles(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Restored) != 1 || result.Restored[0] != "a.txt" {
		t.Fatalf("restored = %#v", result.Restored)
	}
	if len(result.Deleted) != 1 || result.Deleted[0] != "b.txt" {
		t.Fatalf("deleted = %#v", result.Deleted)
	}
	got, err := os.ReadFile(filepath.Join(work, "a.txt"))
	if err != nil || string(got) != original {
		t.Fatalf("a.txt = %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(work, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt should be deleted, err=%v", err)
	}
}

func TestStoreRejectsPathEscape(t *testing.T) {
	work := t.TempDir()
	store, err := NewStore(t.TempDir(), "main", work, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTurn("x"); err != nil {
		t.Fatal(err)
	}
	if err := store.Capture("../outside.txt", nil); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestStorePruneRetain(t *testing.T) {
	work := t.TempDir()
	store, err := NewStore(t.TempDir(), "main", work, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := store.BeginTurn("t"); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2 after prune", len(list))
	}
}

func TestStoreNameAndFindByName(t *testing.T) {
	work := t.TempDir()
	store, err := NewStore(t.TempDir(), "main", work, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTurn("first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTurn("second"); err != nil {
		t.Fatal(err)
	}

	meta, err := store.SetName(-1, "before-refactor")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Turn != 1 || meta.Name != "before-refactor" {
		t.Fatalf("meta = %#v", meta)
	}
	turn, err := store.FindTurnByName("Before-Refactor")
	if err != nil || turn != 1 {
		t.Fatalf("FindTurnByName = %d, %v", turn, err)
	}
	if _, err := store.SetName(0, "before-refactor"); err == nil {
		t.Fatal("expected duplicate name error")
	}
	if _, err := store.SetName(0, "0"); err == nil {
		t.Fatal("expected numeric name rejection")
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[1].Name != "before-refactor" || list[0].Name != "" {
		t.Fatalf("list = %#v", list)
	}
}
