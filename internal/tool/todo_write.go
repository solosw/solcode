package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TodoItem represents a single todo entry.
type TodoItem struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Status     string `json:"status"`   // pending | in_progress | completed
	Priority   string `json:"priority"` // high | medium | low
	ActiveForm string `json:"activeForm,omitempty"`
}

// TodoWriteParams is the input schema for the TodoWrite tool.
type TodoWriteParams struct {
	Todos []TodoItem `json:"todos"`
}

const TodoWriteToolName = "TodoWrite"

type todoWriteTool struct {
	BaseTool
}

// NewTodoWriteTool creates a new task tracking tool.
func NewTodoWriteTool() Tool {
	return &todoWriteTool{}
}

func (t *todoWriteTool) Name() string                      { return TodoWriteToolName }
func (t *todoWriteTool) IsReadOnly(_ json.RawMessage) bool { return false }

func (t *todoWriteTool) Description() string {
	return `Creates and manages a structured task list for the current session.
Use proactively for complex multi-step tasks (3+ steps).

Task states: pending → in_progress → completed
- Exactly ONE task in_progress at a time
- Mark tasks completed IMMEDIATELY after finishing
- Break complex tasks into smaller steps
- Only mark completed when FULLY accomplished

When NOT to use:
- Single straightforward task
- Task is trivial with no organizational benefit
- Under 3 trivial steps`
}

func (t *todoWriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":         map[string]string{"type": "string", "description": "Unique identifier for the todo item"},
						"content":    map[string]string{"type": "string", "description": "Imperative form: what needs to be done (e.g., Run tests)"},
						"status":     map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}, "description": "Current status"},
						"priority":   map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}, "description": "Priority level"},
						"activeForm": map[string]string{"type": "string", "description": "Present continuous form for display (e.g., Running tests)"},
					},
					"required": []string{"id", "content", "status", "priority"},
				},
				"description": "The updated todo list (full replacement).",
			},
		},
		"required": []string{"todos"},
	}
}

func (t *todoWriteTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params TodoWriteParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	// Read old todos
	path := todoPath(uctx)
	_ = readExistingTodos(path) // old state available for history/debugging

	// Check if all done — clear the list
	allDone := len(params.Todos) > 0
	for _, td := range params.Todos {
		if td.Status != "completed" {
			allDone = false
			break
		}
	}

	newTodos := params.Todos
	if allDone {
		newTodos = []TodoItem{}
	}

	// Verification nudge: closing 3+ items without a verification step
	verificationNudge := false
	if allDone && len(params.Todos) >= 3 {
		hasVerif := false
		for _, td := range params.Todos {
			if strings.Contains(strings.ToLower(td.Content), "verif") {
				hasVerif = true
				break
			}
		}
		verificationNudge = !hasVerif
	}

	// Persist to disk
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
	b, _ := json.MarshalIndent(newTodos, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return ErrorResult("failed to write todos: " + err.Error()), nil
	}

	resultText := "Todos updated successfully. Continue tracking your progress."
	if verificationNudge {
		resultText += "\n\nNOTE: You closed 3+ tasks without a verification step. " +
			"Consider verifying your work before finishing."
	}

	// Build a summary of changes
	var changes strings.Builder
	changes.WriteString("Current todos:\n")
	for _, td := range newTodos {
		icon := map[string]string{
			"pending":     "[ ]",
			"in_progress": "[→]",
			"completed":   "[✓]",
		}[td.Status]
		changes.WriteString(fmt.Sprintf("  %s %s (%s)\n", icon, td.Content, td.Status))
	}

	return Result(changes.String()), nil
}

func todoPath(uctx *UseContext) string {
	if uctx != nil && strings.TrimSpace(uctx.TodoPath) != "" {
		return filepath.Clean(uctx.TodoPath)
	}
	workDir := ""
	if uctx != nil {
		workDir = uctx.WorkDir
	}
	return filepath.Join(workDir, ".solcode", "todos.json")
}

func readExistingTodos(path string) []TodoItem {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var todos []TodoItem
	if json.Unmarshal(data, &todos) != nil {
		return nil
	}
	return todos
}
