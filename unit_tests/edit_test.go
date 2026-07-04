package unit_tests

import (
	"testing"

	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestEditTool_Name(t *testing.T) {
	e := tool.NewEditTool()
	if e.Name() != "Edit" {
		t.Fatalf("expected Edit, got %s", e.Name())
	}
	if !e.IsDestructive(nil) {
		t.Fatal("Edit should be destructive")
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"single", 1},
		{"line1\nline2", 2},
		{"a\nb\nc\n", 4},
	}
	for _, tt := range tests {
		if got := tool.CountLines(tt.input); got != tt.expected {
			t.Errorf("CountLines(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}
