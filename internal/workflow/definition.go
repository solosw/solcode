package workflow

import (
	"fmt"
	"strings"

	"github.com/solosw/solcode/internal/tool"
)

// Definition is a user-authored, reusable Task graph loaded from disk.
// Workflows are invoked explicitly (slash command); they are not exposed as a model tool.
type Definition struct {
	Name          string     `json:"name,omitempty" yaml:"name,omitempty"`
	Description   string     `json:"description,omitempty" yaml:"description,omitempty"`
	ExecutionMode string     `json:"execution_mode,omitempty" yaml:"execution_mode,omitempty"`
	Tasks         []TaskSpec `json:"tasks" yaml:"tasks"`

	// Path is the source file on disk (set by the loader, not authored).
	Path string `json:"-" yaml:"-"`
	// Source is the directory the definition was loaded from.
	Source string `json:"-" yaml:"-"`
}

// TaskSpec mirrors tool.TaskItem fields for authoring.
type TaskSpec struct {
	ID           string   `json:"id,omitempty" yaml:"id,omitempty"`
	Description  string   `json:"description" yaml:"description"`
	Prompt       string   `json:"prompt" yaml:"prompt"`
	AllowedTools []string `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"`
	Difficulty   string   `json:"difficulty,omitempty" yaml:"difficulty,omitempty"`
	Model        string   `json:"model,omitempty" yaml:"model,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
}

// Validate checks structural integrity of a workflow definition.
func (d Definition) Validate() error {
	if len(d.Tasks) == 0 {
		return fmt.Errorf("workflow %q has no tasks", d.Name)
	}
	ids := make(map[string]int, len(d.Tasks))
	for i, task := range d.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			id = fmt.Sprintf("task-%d", i+1)
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("workflow %q: duplicate task id %q", d.Name, id)
		}
		ids[id] = i
		if strings.TrimSpace(task.Prompt) == "" {
			return fmt.Errorf("workflow %q: tasks[%d] (%s) prompt is required", d.Name, i, id)
		}
		if strings.TrimSpace(task.Description) == "" {
			return fmt.Errorf("workflow %q: tasks[%d] (%s) description is required", d.Name, i, id)
		}
	}
	for i, task := range d.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			id = fmt.Sprintf("task-%d", i+1)
		}
		for _, dep := range task.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if _, ok := ids[dep]; !ok {
				return fmt.Errorf("workflow %q: task %q depends on unknown id %q", d.Name, id, dep)
			}
			if dep == id {
				return fmt.Errorf("workflow %q: task %q depends on itself", d.Name, id)
			}
		}
	}
	if err := detectCycle(d.Tasks); err != nil {
		return fmt.Errorf("workflow %q: %w", d.Name, err)
	}
	return nil
}

func detectCycle(tasks []TaskSpec) error {
	// Kahn-style: if we cannot schedule all tasks, a cycle exists.
	remaining := make(map[string]TaskSpec, len(tasks))
	for i, task := range tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			id = fmt.Sprintf("task-%d", i+1)
		}
		task.ID = id
		remaining[id] = task
	}
	done := make(map[string]bool, len(tasks))
	for len(remaining) > 0 {
		ready := make([]string, 0)
		for id, task := range remaining {
			ok := true
			for _, dep := range task.DependsOn {
				dep = strings.TrimSpace(dep)
				if dep == "" {
					continue
				}
				if !done[dep] {
					ok = false
					break
				}
			}
			if ok {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			return fmt.Errorf("dependency cycle detected")
		}
		for _, id := range ready {
			done[id] = true
			delete(remaining, id)
		}
	}
	return nil
}

// ToTaskParams converts the definition into Task tool parameters with args substitution.
func (d Definition) ToTaskParams(args string) (tool.TaskParams, error) {
	if err := d.Validate(); err != nil {
		return tool.TaskParams{}, err
	}
	items := make([]tool.TaskItem, 0, len(d.Tasks))
	for i, task := range d.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			id = fmt.Sprintf("task-%d", i+1)
		}
		items = append(items, tool.TaskItem{
			ID:           id,
			Description:  applyArgs(task.Description, args),
			Prompt:       applyArgs(task.Prompt, args),
			AllowedTools: append([]string(nil), task.AllowedTools...),
			Difficulty:   task.Difficulty,
			Model:        task.Model,
			DependsOn:    append([]string(nil), task.DependsOn...),
		})
	}
	return tool.TaskParams{
		Tasks:         items,
		ExecutionMode: d.ExecutionMode,
	}, nil
}

func applyArgs(text, args string) string {
	if !strings.Contains(text, "{{args}}") {
		return text
	}
	return strings.ReplaceAll(text, "{{args}}", args)
}
