package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/sessionmemory"
	"github.com/solosw/solcode/internal/systemone"
	"github.com/solosw/solcode/internal/tool"
)

const (
	maxJudgedTodos = 24
	maxJudgedFiles = 24
)

// sessionTaskContext prefers the current turn prompt, falling back to the
// session summary when the prompt is empty.
func sessionTaskContext(prompt, summary string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt != "" {
		return prompt
	}
	return strings.TrimSpace(summary)
}

func loadSessionTodos(workDir string) []tool.TodoItem {
	path := config.DefaultTodoPath(workDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var todos []tool.TodoItem
	if json.Unmarshal(data, &todos) != nil {
		return nil
	}
	out := make([]tool.TodoItem, 0, len(todos))
	for _, todo := range todos {
		if strings.TrimSpace(todo.ID) == "" && strings.TrimSpace(todo.Content) == "" {
			continue
		}
		out = append(out, todo)
	}
	return out
}

func snapshotTodosWithoutJev(todos []tool.TodoItem) []sessionmemory.TodoJudgment {
	out := make([]sessionmemory.TodoJudgment, 0, len(todos))
	for _, todo := range todos {
		item := sessionmemory.TodoJudgment{
			ID:      strings.TrimSpace(todo.ID),
			Content: strings.TrimSpace(todo.Content),
			Status:  strings.TrimSpace(todo.Status),
			Valid:   true,
			Done:    strings.TrimSpace(todo.Status) == sessionmemory.TodoCompleted,
		}
		out = append(out, item)
	}
	return out
}

// judgeSessionTodos asks Jev which items are still valid for the task and which
// are done. When Jev is off or fails, every item is kept as valid and Done
// follows the model-authored status.
func (j *jevRuntime) judgeSessionTodos(ctx context.Context, task string, todos []tool.TodoItem) []sessionmemory.TodoJudgment {
	base := snapshotTodosWithoutJev(todos)
	if j == nil || j.decider == nil || !j.decider.Enabled() || strings.TrimSpace(task) == "" || len(base) == 0 {
		return base
	}

	limit := len(base)
	if limit > maxJudgedTodos {
		limit = maxJudgedTodos
	}
	candidates := make([]systemone.Candidate, 0, limit)
	byName := make(map[string]int, limit)
	for i := 0; i < limit; i++ {
		todo := base[i]
		name := todoCandidateName(i, todo)
		byName[name] = i
		candidates = append(candidates, systemone.Candidate{
			Name:        name,
			Description: todoCandidateDescription(todo),
		})
	}

	minP := j.routeConfidence()
	valid := j.decider.Screen(ctx, task,
		"Is this todolist item still a valid, useful step for the task in `state`?",
		candidates, minP, 0)
	done := j.decider.Screen(ctx, task,
		"Has this todolist item already been completed for the task in `state`?",
		candidates, minP, 0)

	validSet := map[string]bool{}
	for _, item := range valid {
		validSet[item.Name] = true
	}
	doneSet := map[string]bool{}
	for _, item := range done {
		doneSet[item.Name] = true
	}

	// Fail open on a total miss: keep the model snapshot unchanged.
	if len(valid) == 0 && len(done) == 0 {
		return base
	}
	for name, idx := range byName {
		if len(valid) > 0 {
			base[idx].Valid = validSet[name]
		}
		if doneSet[name] || base[idx].Status == sessionmemory.TodoCompleted {
			base[idx].Done = true
		}
	}
	return base
}

func todoCandidateName(index int, todo sessionmemory.TodoJudgment) string {
	label := strings.TrimSpace(todo.ID)
	if label == "" {
		label = strings.TrimSpace(todo.Content)
	}
	if label == "" {
		label = "todo"
	}
	return fmt.Sprintf("%d:%s", index+1, truncateRunesLocal(label, 80))
}

func todoCandidateDescription(todo sessionmemory.TodoJudgment) string {
	parts := make([]string, 0, 3)
	if todo.ID != "" {
		parts = append(parts, "id="+todo.ID)
	}
	if todo.Status != "" {
		parts = append(parts, "status="+todo.Status)
	}
	if todo.Content != "" {
		parts = append(parts, todo.Content)
	}
	return strings.Join(parts, " | ")
}

// screenSessionFiles drops files that are not important to the task. Rule noise
// is removed first; Jev then screens the remainder when enabled.
func (j *jevRuntime) screenSessionFiles(ctx context.Context, task string, files []string) []string {
	files = filterUnimportantSessionFiles(files)
	if j == nil || j.decider == nil || !j.decider.Enabled() || strings.TrimSpace(task) == "" || len(files) == 0 {
		return files
	}
	limit := len(files)
	if limit > maxJudgedFiles {
		limit = maxJudgedFiles
	}
	candidates := make([]systemone.Candidate, 0, limit)
	byName := make(map[string]string, limit)
	for i := 0; i < limit; i++ {
		name := fmt.Sprintf("%d:%s", i+1, truncateRunesLocal(files[i], 120))
		byName[name] = files[i]
		candidates = append(candidates, systemone.Candidate{
			Name:        name,
			Description: "Changed file path: " + files[i],
		})
	}
	ranked := j.decider.Screen(ctx, task,
		"Is this changed file important to remember for the task in `state`?",
		candidates, j.routeConfidence(), 0)
	if len(ranked) == 0 {
		// Fail open: keep the rule-filtered list.
		return files
	}
	out := make([]string, 0, len(ranked))
	seen := map[string]bool{}
	for _, item := range ranked {
		path := byName[item.Name]
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	// Preserve any files beyond the screening budget.
	for i := limit; i < len(files); i++ {
		if seen[files[i]] {
			continue
		}
		out = append(out, files[i])
	}
	if len(out) == 0 {
		return files
	}
	return out
}

func truncateRunesLocal(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
