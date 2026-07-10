package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/solosw/solcode/internal/agent"
)

const TaskToolName = "Task"

var taskIDCounter uint64

type TaskParams struct {
	Description   string              `json:"description"`
	Prompt        string              `json:"prompt"`
	AllowedTools  []string            `json:"allowed_tools,omitempty"`
	MaxTurns      int                 `json:"max_turns,omitempty"`
	Model         string              `json:"model,omitempty"`
	FastModel     string              `json:"fast_model,omitempty"`
	Tasks         []TaskItem          `json:"tasks,omitempty"`
	ExecutionMode string              `json:"execution_mode,omitempty"`
	Dependencies  map[string][]string `json:"dependencies,omitempty"`
}

type TaskItem struct {
	ID           string   `json:"id,omitempty"`
	Description  string   `json:"description"`
	Prompt       string   `json:"prompt"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	MaxTurns     int      `json:"max_turns,omitempty"`
	Difficulty   string   `json:"difficulty,omitempty"`
	Model        string   `json:"model,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
}

type taskTool struct {
	BaseTool
	coordinator *agent.Coordinator
}

func NewTaskTool(coordinator *agent.Coordinator) Tool {
	return &taskTool{coordinator: coordinator}
}

func (t *taskTool) Name() string { return TaskToolName }
func (t *taskTool) Description() string {
	return `Launches one or more sub-agents to complete independent or dependent tasks and returns their results.
Use a single prompt for one bounded task, or pass tasks with dependency edges. Independent tasks run in parallel; dependency chains run serially by level. Set difficulty=easy or model=fast to use the configured fast model when available.`
}
func (t *taskTool) InputSchema() map[string]any {
	taskSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":            map[string]any{"type": "string", "description": "Stable task id used by dependencies"},
			"description":   map[string]any{"type": "string", "description": "Short label for this task"},
			"prompt":        map[string]any{"type": "string", "description": "Detailed prompt for this sub-agent"},
			"allowed_tools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional list of tools this sub-agent may use"},
			"max_turns":     map[string]any{"type": "integer", "description": "Optional maximum turns for this sub-agent"},
			"difficulty":    map[string]any{"type": "string", "enum": []string{"easy", "medium", "hard"}, "description": "Use easy for fast model, hard for main model"},
			"model":         map[string]any{"type": "string", "description": "Optional explicit model or 'fast'"},
			"depends_on":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Task ids that must complete before this task starts"},
		},
		"required": []string{"description", "prompt"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description":    map[string]any{"type": "string", "description": "Short label for a single task"},
			"prompt":         map[string]any{"type": "string", "description": "Detailed prompt for a single sub-agent"},
			"allowed_tools":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional list of tools the single sub-agent may use"},
			"max_turns":      map[string]any{"type": "integer", "description": "Optional maximum turns for the single sub-agent"},
			"model":          map[string]any{"type": "string", "description": "Optional explicit model or 'fast'"},
			"fast_model":     map[string]any{"type": "string", "description": "Configured fast model id, passed by the host"},
			"execution_mode": map[string]any{"type": "string", "enum": []string{"auto", "parallel", "serial"}, "description": "auto runs independent graph levels in parallel; serial runs tasks one by one"},
			"dependencies":   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "description": "Optional dependency graph keyed by task id"},
			"tasks":          map[string]any{"type": "array", "items": taskSchema, "description": "Multiple tasks to execute"},
		},
	}
}
func (t *taskTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *taskTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *taskTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *taskTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params TaskParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	tasks := normalizeTaskItems(params)
	if len(tasks) == 0 {
		return ErrorResult("description and prompt are required, or provide tasks"), nil
	}
	for i := range tasks {
		if strings.TrimSpace(tasks[i].ID) == "" {
			tasks[i].ID = fmt.Sprintf("task_%d", i+1)
		}
		if strings.TrimSpace(tasks[i].Description) == "" {
			return ErrorResult(fmt.Sprintf("tasks[%d].description is required", i)), nil
		}
		if strings.TrimSpace(tasks[i].Prompt) == "" {
			return ErrorResult(fmt.Sprintf("tasks[%d].prompt is required", i)), nil
		}
	}

	if params.FastModel == "" && uctx != nil {
		params.FastModel = uctx.FastModel
	}
	results, err := t.runTaskGraph(ctx, uctx, tasks, params)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	return Result(formatTaskResults(results)), nil
}

func normalizeTaskItems(params TaskParams) []TaskItem {
	if len(params.Tasks) > 0 {
		tasks := append([]TaskItem(nil), params.Tasks...)
		for i := range tasks {
			if tasks[i].MaxTurns <= 0 {
				tasks[i].MaxTurns = params.MaxTurns
			}
			if tasks[i].Model == "" {
				tasks[i].Model = params.Model
			}
			if len(tasks[i].DependsOn) == 0 && params.Dependencies != nil {
				tasks[i].DependsOn = append([]string(nil), params.Dependencies[tasks[i].ID]...)
			}
		}
		return tasks
	}
	if strings.TrimSpace(params.Description) == "" || strings.TrimSpace(params.Prompt) == "" {
		return nil
	}
	return []TaskItem{{
		ID:           "task_1",
		Description:  params.Description,
		Prompt:       params.Prompt,
		AllowedTools: nil,
		MaxTurns:     params.MaxTurns,
		Model:        params.Model,
	}}
}

type taskRunResult struct {
	ID          string
	Description string
	Output      string
}

func (t *taskTool) runTaskGraph(ctx context.Context, uctx *UseContext, tasks []TaskItem, params TaskParams) ([]taskRunResult, error) {
	remaining := make(map[string]TaskItem, len(tasks))
	completed := map[string]bool{}
	depResults := map[string]string{}
	for _, task := range tasks {
		if remaining[task.ID].ID != "" {
			return nil, fmt.Errorf("duplicate task id: %s", task.ID)
		}
		remaining[task.ID] = task
	}
	serial := strings.EqualFold(params.ExecutionMode, "serial")
	results := make([]taskRunResult, 0, len(tasks))
	for len(remaining) > 0 {
		ready := make([]TaskItem, 0)
		for id, task := range remaining {
			if dependenciesCompleted(task.DependsOn, completed) {
				// Inject predecessor results as context into the prompt.
				if len(task.DependsOn) > 0 {
					task.Prompt = buildDependencyContext(depResults, task.DependsOn) + "\n\n" + task.Prompt
				}
				ready = append(ready, task)
				if serial {
					break
				}
			}
			_ = id
		}
		if len(ready) == 0 {
			return results, fmt.Errorf("task dependency graph has a cycle or missing dependency")
		}
		levelResults, err := t.runTaskLevel(ctx, uctx, ready, params.FastModel)
		if err != nil {
			return results, err
		}
		for _, result := range levelResults {
			completed[result.ID] = true
			depResults[result.ID] = result.Output
			delete(remaining, result.ID)
			results = append(results, result)
		}
	}
	return results, nil
}

func buildDependencyContext(depResults map[string]string, dependsOn []string) string {
	if len(dependsOn) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Previous task results (for context):\n")
	for _, dep := range dependsOn {
		result := strings.TrimSpace(depResults[dep])
		if result == "" {
			continue
		}
		b.WriteString("\n--- Result from task ")
		b.WriteString(dep)
		b.WriteString(" ---\n")
		// Truncate very long results to avoid blowing up context.
		if len(result) > 8000 {
			result = result[:8000] + "\n...[truncated]"
		}
		b.WriteString(result)
	}
	return b.String()
}

func dependenciesCompleted(deps []string, completed map[string]bool) bool {
	for _, dep := range deps {
		if !completed[dep] {
			return false
		}
	}
	return true
}

func (t *taskTool) runTaskLevel(ctx context.Context, uctx *UseContext, tasks []TaskItem, fastModel string) ([]taskRunResult, error) {
	results := make([]taskRunResult, len(tasks))
	errs := make([]error, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		i, task := i, task
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := t.runOneTask(ctx, uctx, task, fastModel)
			results[i] = result
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (t *taskTool) runOneTask(ctx context.Context, uctx *UseContext, task TaskItem, fastModel string) (taskRunResult, error) {
	id := agent.AgentID(fmt.Sprintf("task-%d", atomic.AddUint64(&taskIDCounter, 1)))
	_, err := t.coordinator.Spawn(ctx, agent.AgentConfig{
		ID:           id,
		ParentID:     agent.AgentID(uctx.AgentID),
		Role:         agent.AgentRoleTask,
		Description:  task.Description,
		WorkDir:      uctx.WorkDir,
		Prompt:       task.Prompt,
		AllowedTools: task.AllowedTools,
		MaxTurns:     task.MaxTurns,
		Model:        taskModel(task, fastModel),
	})
	if err != nil {
		return taskRunResult{}, fmt.Errorf("spawn task %s: %w", task.ID, err)
	}
	result, err := t.coordinator.Wait(ctx, id)
	if err != nil {
		return taskRunResult{}, fmt.Errorf("wait task %s: %w", task.ID, err)
	}
	if result.Error != "" {
		return taskRunResult{}, fmt.Errorf("task %s failed: %s", task.ID, result.Error)
	}
	return taskRunResult{ID: task.ID, Description: task.Description, Output: result.Output}, nil
}

func taskModel(task TaskItem, fastModel string) string {
	model := strings.TrimSpace(task.Model)
	if strings.EqualFold(model, "fast") {
		return strings.TrimSpace(fastModel)
	}
	if model != "" {
		return model
	}
	if strings.EqualFold(task.Difficulty, "easy") || strings.EqualFold(task.Difficulty, "fast") {
		return strings.TrimSpace(fastModel)
	}
	return ""
}

func formatTaskResults(results []taskRunResult) string {
	if len(results) == 1 {
		return results[0].Output
	}
	var b strings.Builder
	for _, result := range results {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## ")
		b.WriteString(result.ID)
		if result.Description != "" {
			b.WriteString(" - ")
			b.WriteString(result.Description)
		}
		b.WriteString("\n")
		b.WriteString(result.Output)
	}
	return b.String()
}
