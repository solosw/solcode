package api_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

// TestBashTool_Invoke_API tests the Bash tool end-to-end via its public API.
func TestBashTool_Invoke(t *testing.T) {
	b := tool.NewBashTool()
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: t.TempDir()}

	input, _ := json.Marshal(map[string]interface{}{
		"command": "echo hello world",
	})

	result, err := b.Invoke(context.Background(), uctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	result.Text = strings.TrimSpace(result.Text)
	if result.Text != "hello world" {
		t.Fatalf("expected 'hello world', got '%s'", result.Text)
	}
}

// TestViewTool_Invoke_API tests the View tool via its public API.
func TestViewTool_Invoke(t *testing.T) {
	v := tool.NewViewTool()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0o644)
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: dir}

	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "test.txt",
	})
	result, err := v.Invoke(context.Background(), uctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if result.Text == "" {
		t.Fatal("expected non-empty view result")
	}
	t.Logf("view result: %s", result.Text)
}

// TestWriteAndEdit_API tests the Write → Edit workflow.
func TestWriteAndEdit_API(t *testing.T) {
	dir := t.TempDir()
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: dir}

	// Write a file
	wt := tool.NewWriteTool()
	writeInput, _ := json.Marshal(map[string]interface{}{
		"file_path": "hello.go",
		"content":   "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
	})
	result, err := wt.Invoke(context.Background(), uctx, writeInput)
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if result.IsError {
		t.Fatalf("write failed: %s", result.Text)
	}

	// Edit the file
	et := tool.NewEditTool()
	editInput, _ := json.Marshal(map[string]interface{}{
		"file_path":  filepath.Join(dir, "hello.go"),
		"old_string": "println(\"hello\")",
		"new_string": "println(\"hello, world\")",
	})
	result, err = et.Invoke(context.Background(), uctx, editInput)
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	if result.IsError {
		t.Fatalf("edit failed: %s", result.Text)
	}

	// Verify
	data, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	content := string(data)
	if content != "package main\n\nfunc main() {\n\tprintln(\"hello, world\")\n}\n" {
		t.Fatalf("unexpected content after edit:\n%s", content)
	}
}

// TestGlobTool_Invoke_API tests the Glob tool via its public API.
func TestGlobTool_Invoke(t *testing.T) {
	g := tool.NewGlobTool()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b"), 0o644)
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: dir}

	input, _ := json.Marshal(map[string]interface{}{
		"pattern": "*.go",
	})
	result, err := g.Invoke(context.Background(), uctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	t.Logf("glob result: %s", result.Text)
}

// TestLsTool_Invoke_API tests the LS tool via its public API.
func TestLsTool_Invoke(t *testing.T) {
	l := tool.NewLsTool()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: dir}

	input, _ := json.Marshal(map[string]interface{}{
		"path": dir,
	})
	result, err := l.Invoke(context.Background(), uctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if result.Text == "" {
		t.Fatal("expected non-empty ls result")
	}
	t.Logf("ls result:\n%s", result.Text)
}

// TestGrepTool_Invoke_API tests the Grep tool via its public API.
func TestGrepTool_Invoke(t *testing.T) {
	g := tool.NewGrepTool()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: dir}

	input, _ := json.Marshal(map[string]interface{}{
		"pattern":      "func main",
		"literal_text": true,
	})
	result, err := g.Invoke(context.Background(), uctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	t.Logf("grep result: %s", result.Text)
}

// TestRegistry_Integration tests the full tool registration flow.
func TestRegistry_Integration(t *testing.T) {
	reg := tool.NewRegistry()

	// Register all standard tools
	reg.Register(
		tool.NewBashTool(),
		tool.NewViewTool(),
		tool.NewEditTool(),
		tool.NewWriteTool(),
		tool.NewGlobTool(),
		tool.NewGrepTool(),
		tool.NewLsTool(),
	)

	if reg.Len() != 7 {
		t.Fatalf("expected 7 tools, got %d", reg.Len())
	}

	// Check all tools are sorted
	all := reg.All()
	for i := 1; i < len(all); i++ {
		if all[i-1].Name() >= all[i].Name() {
			t.Fatalf("tools not sorted: %s >= %s", all[i-1].Name(), all[i].Name())
		}
	}

	// Filter read-only tools
	roTools := 0
	for _, t := range all {
		if t.IsReadOnly(nil) {
			roTools++
		}
	}
	if roTools < 4 {
		t.Fatalf("expected at least 4 read-only tools, got %d", roTools)
	}

	t.Logf("registered %d tools", reg.Len())
	for _, tool := range all {
		t.Logf("  %s (readonly=%v, destructive=%v)", tool.Name(), tool.IsReadOnly(nil), tool.IsDestructive(nil))
	}
}
