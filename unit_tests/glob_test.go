package unit_tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestGlobTool_ReadOnly(t *testing.T) {
	g := tool.NewGlobTool()
	if !g.IsReadOnly(nil) {
		t.Fatal("Glob should be read-only")
	}
	if !g.IsConcurrencySafe(nil) {
		t.Fatal("Glob should be concurrency-safe")
	}
}

func TestSkipHiddenPath(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/src/.hidden/file.go", true},
		{"/src/sub/.secret/config", true},
		{"/src/main.go", false},
		{"/src/subdir/file.go", false},
		{"relative/path.go", false},
	}
	for _, tt := range tests {
		if got := tool.SkipHiddenPath(tt.path); got != tt.expected {
			t.Errorf("SkipHiddenPath(%s) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestGlobWithDoublestar(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("pkg a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("pkg b"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("text"), 0o644)

	results, truncated, err := tool.GlobWithDoublestar("*.go", dir, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated || len(results) != 2 {
		t.Fatalf("expected 2 results not truncated, got %d (truncated=%v)", len(results), truncated)
	}
}

func TestGlobWithDoublestar_Limit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		f, _ := os.CreateTemp(dir, "test-*.txt")
		f.Close()
	}
	results, truncated, err := tool.GlobWithDoublestar("*.txt", dir, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !truncated || len(results) != 2 {
		t.Fatalf("expected truncated to 2, got %d (truncated=%v)", len(results), truncated)
	}
}
