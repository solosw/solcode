package unit_tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestViewTool_ReadOnly(t *testing.T) {
	vt := tool.NewViewTool()
	if vt.Name() != "View" {
		t.Fatalf("expected View, got %s", vt.Name())
	}
	if !vt.IsReadOnly(nil) {
		t.Fatal("View should be read-only")
	}
}

func TestAddLineNumbers(t *testing.T) {
	result := tool.AddLineNumbers("line1\nline2\nline3", 1)
	expected := "     1|line1\n     2|line2\n     3|line3"
	if result != expected {
		t.Fatalf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestAddLineNumbers_Offset(t *testing.T) {
	result := tool.AddLineNumbers("a\nb", 10)
	expected := "    10|a\n    11|b"
	if result != expected {
		t.Fatalf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestAddLineNumbers_Empty(t *testing.T) {
	if tool.AddLineNumbers("", 1) != "" {
		t.Fatal("expected empty string")
	}
}

func TestIsImagePath(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"photo.jpg", true}, {"icon.png", true}, {"logo.svg", true},
		{"script.go", false}, {"readme.md", false}, {"main.py", false},
	}
	for _, tt := range tests {
		if got := tool.IsImagePath(tt.path); got != tt.expected {
			t.Errorf("IsImagePath(%s) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestSuggestSimilarFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)

	result := tool.SuggestSimilarFile(filepath.Join(dir, "mainx.go"))
	if !result.IsError {
		t.Fatal("expected error for missing file")
	}
	t.Log(result.Text)
}

func TestReadTextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\nline4\nline5"), 0o644)

	content, total, err := tool.ReadTextFile(path, 0, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total <= 0 {
		t.Fatalf("expected positive line count, got %d", total)
	}
	if content == "" {
		t.Fatal("expected non-empty content")
	}
	t.Logf("content: %s (total=%d)", content, total)
}
