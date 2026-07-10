package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestTodoWriteTool_Invoke(t *testing.T) {
	tw := tool.NewTodoWriteTool()
	if tw.Name() != "TodoWrite" {
		t.Fatalf("expected TodoWrite, got %s", tw.Name())
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: dir, TodoPath: path}

	todos := []tool.TodoItem{
		{ID: "1", Content: "Write tests", Status: "in_progress", Priority: "high", ActiveForm: "Writing tests"},
		{ID: "2", Content: "Fix bug", Status: "pending", Priority: "medium", ActiveForm: "Fixing bug"},
		{ID: "3", Content: "Review code", Status: "pending", Priority: "low", ActiveForm: "Reviewing code"},
	}

	input, _ := json.Marshal(map[string]interface{}{
		"todos": todos,
	})

	result, err := tw.Invoke(context.Background(), uctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Text)
	}
	if result.Text == "" {
		t.Fatal("expected non-empty result")
	}
	t.Logf("result: %s", result.Text)

	// Verify file was written
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("todos file not created: %v", err)
	}
	t.Logf("todos file:\n%s", string(data))

	var saved []tool.TodoItem
	json.Unmarshal(data, &saved)
	if len(saved) != 3 {
		t.Fatalf("expected 3 todos saved, got %d", len(saved))
	}
}

func TestTodoWriteTool_AllDone(t *testing.T) {
	tw := tool.NewTodoWriteTool()
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: dir, TodoPath: path}

	todos := []tool.TodoItem{
		{ID: "1", Content: "Task one", Status: "completed", Priority: "high"},
		{ID: "2", Content: "Task two", Status: "completed", Priority: "medium"},
	}

	input, _ := json.Marshal(map[string]interface{}{
		"todos": todos,
	})

	result, _ := tw.Invoke(context.Background(), uctx, input)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}

	// All completed → should clear
	data, _ := os.ReadFile(path)
	var saved []tool.TodoItem
	json.Unmarshal(data, &saved)
	if len(saved) != 0 {
		t.Fatalf("expected empty todos when all done, got %d", len(saved))
	}
}

func TestTodoWriteTool_FallsBackToWorkDirTodos(t *testing.T) {
	tw := tool.NewTodoWriteTool()
	dir := t.TempDir()
	uctx := &tool.UseContext{SessionID: "test", MessageID: "msg1", WorkDir: dir}

	input, _ := json.Marshal(map[string]interface{}{
		"todos": []tool.TodoItem{{ID: "1", Content: "Legacy path", Status: "pending", Priority: "medium"}},
	})

	result, err := tw.Invoke(context.Background(), uctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Text)
	}

	path := filepath.Join(dir, ".solcode", "todos.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("todos file not created at fallback path: %v", err)
	}
	var saved []tool.TodoItem
	json.Unmarshal(data, &saved)
	if len(saved) != 1 || saved[0].Content != "Legacy path" {
		t.Fatalf("unexpected saved todos: %#v", saved)
	}
}
