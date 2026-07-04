package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/solosw/codeplus-agent/internal/agent"
)

const TaskToolName = "Task"

var taskIDCounter uint64

type TaskParams struct {
	Description  string   `json:"description"`
	Prompt       string   `json:"prompt"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	MaxTurns     int      `json:"max_turns,omitempty"`
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
	return `Launches a sub-agent to complete an independent task and returns its final result.
Use this for bounded research, review, or analysis work that can run with its own prompt and tool allowlist.`
}
func (t *taskTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{
				"type":        "string",
				"description": "Short label for the task",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "Detailed prompt for the sub-agent",
			},
			"allowed_tools": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional list of tools the sub-agent may use",
			},
			"max_turns": map[string]any{
				"type":        "integer",
				"description": "Optional maximum turns for the sub-agent",
			},
		},
		"required": []string{"description", "prompt"},
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
	if params.Description == "" {
		return ErrorResult("description is required"), nil
	}
	if params.Prompt == "" {
		return ErrorResult("prompt is required"), nil
	}

	id := agent.AgentID(fmt.Sprintf("task-%d", atomic.AddUint64(&taskIDCounter, 1)))
	_, err := t.coordinator.Spawn(ctx, agent.AgentConfig{
		ID:           id,
		ParentID:     agent.AgentID(uctx.AgentID),
		Role:         agent.AgentRoleTask,
		Description:  params.Description,
		WorkDir:      uctx.WorkDir,
		Prompt:       params.Prompt,
		AllowedTools: params.AllowedTools,
		MaxTurns:     params.MaxTurns,
	})
	if err != nil {
		return ErrorResult("spawn task agent: " + err.Error()), nil
	}

	result, err := t.coordinator.Wait(ctx, id)
	if err != nil {
		return ErrorResult("wait task agent: " + err.Error()), nil
	}
	if result.Error != "" {
		return ErrorResult(result.Error), nil
	}
	return Result(result.Output), nil
}
