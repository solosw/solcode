package unit_tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestLsTool_ReadOnly(t *testing.T) {
	ls := tool.NewLsTool()
	if !ls.IsReadOnly(nil) {
		t.Fatal("LS should be read-only")
	}
}

func TestLsTool_Aliases(t *testing.T) {
	ls := tool.NewLsTool()
	for _, a := range ls.Aliases() {
		if a == "List" {
			return
		}
	}
	t.Fatal("LS should have 'List' alias")
}

func TestListDirectory(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b"), 0o644)
	os.WriteFile(filepath.Join(dir, "subdir", "c.txt"), []byte("c"), 0o644)

	results, truncated, err := tool.ListDirectory(context.Background(), dir, nil, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated || len(results) < 3 {
		t.Fatalf("expected >=3 results, got %d (truncated=%v)", len(results), truncated)
	}
}

func TestListDirectory_SkipsHidden(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("hi"), 0o644)

	results, _, err := tool.ListDirectory(context.Background(), dir, nil, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range results {
		base := filepath.Base(r)
		if base == ".hidden" || base == ".git" {
			t.Fatalf("hidden file/dir should be skipped: %s", r)
		}
	}
}

func TestShouldSkipLS(t *testing.T) {
	if !tool.ShouldSkipLS("/foo/.hidden", nil) {
		t.Fatal(".hidden should be skipped")
	}
	if tool.ShouldSkipLS("/foo/source.go", nil) {
		t.Fatal("source.go should not be skipped")
	}
	if !tool.ShouldSkipLS("/foo/ignore_me.txt", []string{"ignore_*"}) {
		t.Fatal("ignore_* should match ignore_me.txt")
	}
}
