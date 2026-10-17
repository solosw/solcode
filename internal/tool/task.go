package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/agent"
)

const TaskToolName = "Task"

var taskRetryDelay = 10 * time.Second

const taskMaxRetries = 5

type TaskParams struct {
	Action                  string `json:"action,omitempty"`
	HistoryKind             string `json:"history_kind,omitempty"`
	Offset                  int    `json:"offset,omitempty"`
	Limit                   int    `json:"limit,omitempty"`
	AgentID                 string `json:"agent_id,omitempty"`
	ForkContext             string `json:"fork_context,omitempty"`
	ForkLastN               int    `json:"fork_last_n,omitempty"`
	AcknowledgeInterruption bool   `json:"acknowledge_interruption,omitempty"`

	Description     string              `json:"description"`
	Prompt          string              `json:"prompt"`
	AllowedTools    []string            `json:"allowed_tools,omitempty"`
	Model           string              `json:"model,omitempty"`
	FastModel       string              `json:"fast_model,omitempty"`
	Tasks           []TaskItem          `json:"tasks,omitempty"`
	ExecutionMode   string              `json:"execution_mode,omitempty"`
	Dependencies    map[string][]string `json:"dependencies,omitempty"`
	RunInBackground bool                `json:"run_in_background,omitempty"`
}

type TaskItem struct {
	AgentID                 string `json:"agent_id,omitempty"`
	ForkContext             string `json:"fork_context,omitempty"`
	ForkLastN               int    `json:"fork_last_n,omitempty"`
	AcknowledgeInterruption bool   `json:"acknowledge_interruption,omitempty"`

	ID           string   `json:"id,omitempty"`
	Description  string   `json:"description"`
	Prompt       string   `json:"prompt"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Difficulty   string   `json:"difficulty,omitempty"`
	Model        string   `json:"model,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
}

type taskTool struct {
	BaseTool
	subagent    Tool
	coordinator *agent.Coordinator
}

func NewTaskTool(coordinator *agent.Coordinator) Tool {
	return NewTaskToolWithSubagent(NewSubagentTool(coordinator))
}

// NewTaskToolWithSubagent wires Task orchestration to an existing Subagent tool.
func NewTaskToolWithSubagent(subagent Tool) Tool {
	t := &taskTool{subagent: subagent}
	if runner, ok := subagent.(*subagentTool); ok {
		t.coordinator = runner.coordinator
	}
	return t
}

func (t *taskTool) Name() string { return TaskToolName }
func (t *taskTool) Description() string {
	return `Launches one or more sub-agents to complete independent or dependent tasks and returns their results. Available only in explicit ultra mode (bypass + task).
When enabled by the host (default), the main agent yields long-running Task calls after the host foreground interval (default 10s). Set run_in_background=true to yield immediately when background scheduling is enabled. The entire task graph keeps running, and its final result is delivered automatically as a completion notification. Continue only independent work; do not poll, relaunch the task, or use unfinished results.
Use a single prompt for one bounded task, or pass tasks with dependency edges. Independent tasks run in parallel; dependency chains run serially by level. Set difficulty=easy or model=fast to use the configured fast model when available.
Use action=spawn to create a reusable child, or action=resume with agent_id to continue it. Children are shared by sessions in the same project working directory (including across restarts when session persistence is enabled). For compatibility, omitted action or action=run creates a child without agent_id and resumes one with agent_id. Task results include agent_id and run_id. A busy, archived, foreign or unknown id is an error, never a replacement agent. Use action=list/status/history/wait/cancel/archive to manage children without launching a model request. fork_context=none (default), last_n, full, or working_set only applies at creation; existing agent history is always reused. Current permissions apply on resume, and allowed_tools can narrow but never expand the original ceiling. After a crash with unconfirmed tool outcomes, inspect history and reconcile external state before explicitly acknowledging the interruption.
Each leaf task runs through the internal Subagent tool (including safe pre-tool retries). Give each sub-agent a self-contained prompt with paths, constraints, and the expected return. Do not use Task for a single cheap file read you can do with View/Grep operations in BatchTool.

allowed_tools is a RUNTIME permission on the child executor only. It does not
change the tools schema sent to the model: every agent (main and sub) shares
the same core tools prefix for prompt-cache stability. Disallowed tools still
appear in the schema but fail at invoke time if called. For BatchTool operations, list permitted child tool names (e.g. View/Grep) in allowed_tools; BatchTool alone does not authorize its children.`
}
func (t *taskTool) InputSchema() map[string]any {
	taskSchema := map[string]any{
		"type": "object",
		"properties": agentReuseSchema(map[string]any{
			"id":            map[string]any{"type": "string", "description": "Stable task id used by dependencies"},
			"description":   map[string]any{"type": "string", "description": "Short label for this task"},
			"prompt":        map[string]any{"type": "string", "description": "Detailed prompt for this sub-agent"},
			"allowed_tools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Runtime allowlist for this sub-agent's executor. Does not change the shared tools schema prefix (core tools stay on the wire for prompt cache). Empty means unrestricted execution (subject to permission mode)."},
			"difficulty":    map[string]any{"type": "string", "enum": []string{"easy", "medium", "hard"}, "description": "Use easy for fast model, hard for main model"},
			"model":         map[string]any{"type": "string", "description": "Optional explicit model or 'fast'"},
			"depends_on":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Task ids that must complete before this task starts"},
		}),
		"required": []string{"description", "prompt"},
	}
	return map[string]any{
		"type": "object",
		"properties": agentReuseSchema(map[string]any{
			"action":            map[string]any{"type": "string", "enum": []string{"spawn", "resume", "run", "list", "status", "history", "wait", "cancel", "archive"}, "description": "spawn creates new children; resume requires agent_id on each task. Default/legacy run infers spawn or resume from agent_id. list shows all children in the current project working directory, across sessions. Management actions do not call a model."},
			"history_kind":      map[string]any{"type": "string", "enum": []string{"messages", "runs", "events"}, "description": "History stream to page (default messages); offset and limit apply to this stream."},
			"offset":            map[string]any{"type": "integer", "minimum": 0, "description": "History message offset (default 0)"},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "History page size (default 20)"},
			"run_in_background": map[string]any{"type": "boolean", "description": "Yield immediately while this task graph continues; receive a completion notification automatically (main agent, when host background scheduling is enabled)."},
			"description":       map[string]any{"type": "string", "description": "Short label for a single task"},
			"prompt":            map[string]any{"type": "string", "description": "Detailed prompt for a single sub-agent"},
			"allowed_tools":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Runtime allowlist for the single sub-agent's executor. Does not change the shared tools schema prefix. Empty means unrestricted execution (subject to permission mode)."},
			"model":             map[string]any{"type": "string", "description": "Optional explicit model or 'fast'"},
			"fast_model":        map[string]any{"type": "string", "description": "Configured fast model id, passed by the host"},
			"execution_mode":    map[string]any{"type": "string", "enum": []string{"auto", "parallel", "serial"}, "description": "auto runs independent graph levels in parallel; serial runs tasks one by one"},
			"dependencies":      map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "description": "Optional dependency graph keyed by task id"},
			"tasks":             map[string]any{"type": "array", "items": taskSchema, "description": "Multiple tasks to execute"},
		}),
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
	if uctx != nil && uctx.AgentRole != "" && uctx.AgentRole != "main" {
		return ErrorResult("nested Task calls are not allowed"), nil
	}
	params.AgentID = strings.TrimSpace(params.AgentID)
	switch params.Action {
	case "", "run", "spawn", "resume":
	case "list", "status", "history", "wait", "cancel", "archive":
		return t.manage(ctx, uctx, params)
	default:
		return ErrorResult("unknown Task action: " + params.Action), nil
	}
	if len(params.Tasks) > 0 && params.AgentID != "" {
		return ErrorResult("with tasks, set agent_id on each task item"), nil
	}
	tasks := normalizeTaskItems(params)
	if len(tasks) == 0 {
		return ErrorResult("description and prompt are required, or provide tasks"), nil
	}
	for i := range tasks {
		tasks[i].AgentID = strings.TrimSpace(tasks[i].AgentID)
		if params.Action == "spawn" && tasks[i].AgentID != "" {
			return ErrorResult("spawn must not specify agent_id; use resume to continue an existing agent"), nil
		}
		if params.Action == "resume" && tasks[i].AgentID == "" {
			return ErrorResult(fmt.Sprintf("tasks[%d].agent_id is required for resume", i)), nil
		}
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
			if tasks[i].ForkContext == "" {
				tasks[i].ForkContext = params.ForkContext
				tasks[i].ForkLastN = params.ForkLastN
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
		ID:      "task_1",
		AgentID: params.AgentID, ForkContext: params.ForkContext, ForkLastN: params.ForkLastN, AcknowledgeInterruption: params.AcknowledgeInterruption,
		Description:  params.Description,
		Prompt:       params.Prompt,
		AllowedTools: params.AllowedTools,
		Model:        params.Model,
	}}
}

type taskRunResult struct {
	AgentID     string
	RunID       string
	ID          string
	Description string
	Output      string
}

func (t *taskTool) runTaskGraph(ctx context.Context, uctx *UseContext, tasks []TaskItem, params TaskParams) ([]taskRunResult, error) {
	remaining := make(map[string]TaskItem, len(tasks))
	completed := map[string]bool{}
	depResults := map[string]string{}
	depAgents := map[string]string{}
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
					missing := make([]string, 0, len(task.DependsOn))
					for _, dep := range task.DependsOn {
						if task.AgentID == "" || depAgents[dep] != task.AgentID {
							missing = append(missing, dep)
						}
					}
					if len(missing) > 0 {
						task.Prompt = buildDependencyContext(depResults, missing) + "\n\n" + task.Prompt
					}
				}
				if task.AgentID != "" {
					for _, other := range ready {
						if other.AgentID == task.AgentID {
							return results, fmt.Errorf("agent %s is assigned concurrent tasks; add a dependency or use serial execution", task.AgentID)
						}
					}
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
			depAgents[result.ID] = result.AgentID
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
	levelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type taskOutcome struct {
		index  int
		result taskRunResult
		err    error
	}
	outcomes := make(chan taskOutcome, len(tasks))
	for i, task := range tasks {
		go func(index int, task TaskItem) {
			result, err := t.runOneTask(levelCtx, uctx, task, fastModel)
			outcomes <- taskOutcome{index: index, result: result, err: err}
		}(i, task)
	}

	results := make([]taskRunResult, len(tasks))
	for range tasks {
		outcome := <-outcomes
		if outcome.err != nil {
			cancel()
			return nil, outcome.err
		}
		results[outcome.index] = outcome.result
	}
	return results, nil
}

func (t *taskTool) runOneTask(ctx context.Context, uctx *UseContext, task TaskItem, fastModel string) (taskRunResult, error) {
	if t.subagent == nil {
		return taskRunResult{}, fmt.Errorf("task %s: subagent tool is not configured", task.ID)
	}
	payload, err := json.Marshal(SubagentParams{
		AgentID: task.AgentID, ForkContext: task.ForkContext, ForkLastN: task.ForkLastN, AcknowledgeInterruption: task.AcknowledgeInterruption,
		Description:  task.Description,
		Prompt:       task.Prompt,
		AllowedTools: task.AllowedTools,
		Model:        task.Model,
		Difficulty:   task.Difficulty,
		FastModel:    fastModel,
		TaskID:       task.ID,
	})
	if err != nil {
		return taskRunResult{}, fmt.Errorf("task %s: encode subagent params: %w", task.ID, err)
	}
	result, err := t.subagent.Invoke(ctx, uctx, payload)
	if err != nil {
		return taskRunResult{}, fmt.Errorf("task %s: %w", task.ID, err)
	}
	if result == nil {
		return taskRunResult{}, fmt.Errorf("task %s: subagent returned no result", task.ID)
	}
	if result.IsError {
		return taskRunResult{}, fmt.Errorf("%s (agent_id=%s run_id=%s)", strings.TrimSpace(result.Text), result.AgentID, result.RunID)
	}
	return taskRunResult{ID: task.ID, AgentID: result.AgentID, RunID: result.RunID, Description: task.Description, Output: result.Text}, nil
}

func formatTaskResults(results []taskRunResult) string {
	if len(results) == 1 {
		return taskResultText(results[0])
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
		b.WriteString(taskResultText(result))
	}
	return b.String()
}
