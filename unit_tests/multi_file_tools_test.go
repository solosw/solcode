package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestMultiEditAppliesOrderedEditsAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	if err := os.WriteFile(first, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := `{"edits":[
		{"path":"first.txt","old_string":"alpha","new_string":"ALPHA","desc":"capitalize alpha"},
		{"path":"first.txt","old_string":"beta","new_string":"BETA","desc":"capitalize beta"},
		{"path":"second.txt","old_string":"two","new_string":"TWO","desc":"capitalize two"}
	]}`
	var changes []tool.FileChange
	content, err := tool.NewMultiEditTool().Invoke(context.Background(), &tool.UseContext{
		WorkDir: dir,
		RecordFileChange: func(_ context.Context, change tool.FileChange) {
			changes = append(changes, change)
		},
	}, json.RawMessage(input))
	if err != nil || content.IsError {
		t.Fatalf("Invoke() = %#v, %v", content, err)
	}
	assertFileContent(t, first, "ALPHA\nBETA\ngamma\n")
	assertFileContent(t, second, "one\nTWO\n")
	if len(changes) != 2 || changes[0].ToolName != tool.MultiEditToolName || changes[1].ToolName != tool.MultiEditToolName {
		t.Fatalf("changes = %#v", changes)
	}
	if !strings.Contains(content.Text, "Applied 3 edits across 2 files") {
		t.Fatalf("result = %q", content.Text)
	}
}

func TestMultiEditDoesNotWriteWhenAnyEditIsInvalid(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	if err := os.WriteFile(first, []byte("before-first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("before-second"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := `{"atomic":true,"edits":[
		{"path":"first.txt","old_string":"before-first","new_string":"after-first"},
		{"path":"second.txt","old_string":"missing","new_string":"after-second"}
	]}`
	content, err := tool.NewMultiEditTool().Invoke(context.Background(), &tool.UseContext{WorkDir: dir}, json.RawMessage(input))
	if err != nil || !content.IsError {
		t.Fatalf("Invoke() = %#v, %v", content, err)
	}
	assertFileContent(t, first, "before-first")
	assertFileContent(t, second, "before-second")
}

func TestMultiWriteWritesMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	input := `{"files":[
		{"path":"nested/one.txt","content":"one","desc":"create first file"},
		{"path":"nested/two.txt","content":"two","desc":"create second file"}
	]}`
	content, err := tool.NewMultiWriteTool().Invoke(context.Background(), &tool.UseContext{WorkDir: dir}, json.RawMessage(input))
	if err != nil || content.IsError {
		t.Fatalf("Invoke() = %#v, %v", content, err)
	}
	assertFileContent(t, filepath.Join(dir, "nested", "one.txt"), "one")
	assertFileContent(t, filepath.Join(dir, "nested", "two.txt"), "two")
	if !strings.Contains(content.Text, "Wrote 2 files") {
		t.Fatalf("result = %q", content.Text)
	}
}

func TestMultiWriteRejectsDuplicatePathsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	input := `{"files":[
		{"path":"same.txt","content":"first"},
		{"path":"same.txt","content":"second"}
	]}`
	content, err := tool.NewMultiWriteTool().Invoke(context.Background(), &tool.UseContext{WorkDir: dir}, json.RawMessage(input))
	if err != nil || !content.IsError {
		t.Fatalf("Invoke() = %#v, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "same.txt")); !os.IsNotExist(err) {
		t.Fatalf("same.txt exists or stat failed: %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
