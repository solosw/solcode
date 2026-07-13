package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/tool"
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

func TestEditToolRecordsDescribedChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	var recorded tool.FileChange
	content, err := tool.NewEditTool().Invoke(context.Background(), &tool.UseContext{
		WorkDir: filepath.Dir(path),
		RecordFileChange: func(_ context.Context, change tool.FileChange) {
			recorded = change
		},
	}, json.RawMessage(`{"file_path":"notes.txt","old_string":"before","new_string":"after","desc":"update notes"}`))
	if err != nil || content.IsError {
		t.Fatalf("Invoke() = %#v, %v", content, err)
	}
	if recorded.ToolName != tool.EditToolName || recorded.Description != "update notes" || recorded.Before != "before" || recorded.After != "after" {
		t.Fatalf("recorded change = %#v", recorded)
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
