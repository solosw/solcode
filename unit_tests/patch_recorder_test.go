package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestPatchToolRecordsDescribedChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var recorded tool.FileChange
	content, err := tool.NewPatchTool().Invoke(context.Background(), &tool.UseContext{
		WorkDir: filepath.Dir(path),
		RecordFileChange: func(_ context.Context, change tool.FileChange) {
			recorded = change
		},
	}, json.RawMessage(`{"file_path":"notes.txt","patch_text":"@@ -1,1 +1,1 @@\n-before\n+after","desc":"update notes"}`))
	if err != nil || content.IsError {
		t.Fatalf("Invoke() = %#v, %v", content, err)
	}
	if recorded.ToolName != tool.PatchToolName || recorded.Description != "update notes" || recorded.Before != "before\n" || recorded.After != "after" {
		t.Fatalf("recorded change = %#v", recorded)
	}
}
