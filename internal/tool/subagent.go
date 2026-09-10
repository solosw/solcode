package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/solosw/solcode/internal/agent"
)

const SubagentToolName = "Subagent"

var subagentIDCounter uint64

// SubagentParams runs a single child agent. Task orchestrates multiple of these.
type SubagentParams struct {
	Description  string   `json:"description"`
	Prompt       string   `json:"prompt"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Model        string   `json:"model,omitempty"`
	Difficulty   string   `json:"difficulty,omitempty"`
	// FastModel is filled by the host / Task; not part of the model-facing schema.
	FastModel string `json:"fast_model,omitempty"`
	// TaskID is an optional orchestration id from Task (for progress correlation).
	TaskID string `json:"task_id,omitempty"`
}

type subagentTool struct {
	BaseTool
	coordinator *agent.Coordinator
}

// NewSubagentTool creates the internal single-subagent runner.
func NewSubagentTool(coordinator *agent.Coordinator) Tool {
	return &subagentTool{coordinator: coordinator}
}

func (t *subagentTool) Name() string { return SubagentToolName }

func (t *subagentTool) Description() string {
	return `Internal: run one sub-agent to completion (with retries).

Task orchestrates one or more Subagent calls. Models should use Task, not this
tool directly. Pass a self-contained prompt with paths, constraints, and the
expected return value.`
}

func (t *subagentTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description":   map[string]any{"type": "string", "description": "Short label for this sub-agent"},
			"prompt":        map[string]any{"type": "string", "description": "Detailed prompt for the sub-agent"},
			"allowed_tools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tool allowlist for this sub-agent"},
			"model":         map[string]any{"type": "string", "description": "Optional explicit model or 'fast'"},
			"difficulty":    map[string]any{"type": "string", "enum": []string{"easy", "medium", "hard"}, "description": "Use easy for the configured fast model"},
			"task_id":       map[string]any{"type": "string", "description": "Optional orchestration id from Task"},
		},
		"required": []string{"description", "prompt"},
	}
}

func (t *subagentTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *subagentTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *subagentTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *subagentTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params SubagentParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	params.Description = strings.TrimSpace(params.Description)
	params.Prompt = strings.TrimSpace(params.Prompt)
	params.TaskID = strings.TrimSpace(params.TaskID)
	if params.Description == "" || params.Prompt == "" {
		return ErrorResult("description and prompt are required"), nil
	}
	if params.FastModel == "" && uctx != nil {
		params.FastModel = uctx.FastModel
	}
	if t.coordinator == nil {
		return ErrorResult("subagent coordinator is not configured"), nil
	}

	output, err := t.runWithRetry(ctx, uctx, params)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	return Result(output), nil
}

func (t *subagentTool) runWithRetry(ctx context.Context, uctx *UseContext, params SubagentParams) (string, error) {
	retryDelay := taskRetryDelay
	if uctx != nil && uctx.TaskRetryDelay > 0 {
		retryDelay = uctx.TaskRetryDelay
	}

	label := params.Description
	if params.TaskID != "" {
		label = params.TaskID
	}

	var lastErr error
	for attempt := 0; attempt <= taskMaxRetries; attempt++ {
		output, err := t.runAttempt(ctx, uctx, params)
		if err == nil {
			return output, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		lastErr = err
		if attempt == taskMaxRetries {
			break
		}
		retry := attempt + 1
		emitAgentProgress(uctx, AgentProgressEvent{
			Kind:            "retry",
			ParentAgentID:   parentAgentID(uctx),
			ParentToolUseID: parentToolUseID(uctx),
			TaskID:          params.TaskID,
			Description:     params.Description,
			Output:          fmt.Sprintf("retry %d/%d: %s", retry, taskMaxRetries, params.Description),
		})
		if err := waitForTaskRetry(ctx, retryDelay); err != nil {
			return "", fmt.Errorf("task %s canceled: %w", label, err)
		}
	}
	return "", lastErr
}

func (t *subagentTool) runAttempt(ctx context.Context, uctx *UseContext, params SubagentParams) (string, error) {
	label := params.TaskID
	if label == "" {
		label = params.Description
	}
	parentID := parentAgentID(uctx)
	workDir := ""
	parentTool := parentToolUseID(uctx)
	if uctx != nil {
		workDir = uctx.WorkDir
	}
	id := agent.AgentID(fmt.Sprintf("task-%d", atomic.AddUint64(&subagentIDCounter, 1)))
	emitAgentProgress(uctx, AgentProgressEvent{
		Kind:            "started",
		AgentID:         string(id),
		ParentAgentID:   parentID,
		ParentToolUseID: parentTool,
		TaskID:          params.TaskID,
		Description:     params.Description,
	})
	_, err := t.coordinator.Spawn(ctx, agent.AgentConfig{
		ID:              id,
		ParentID:        agent.AgentID(parentID),
		Role:            agent.AgentRoleTask,
		Description:     params.Description,
		WorkDir:         workDir,
		Prompt:          params.Prompt,
		AllowedTools:    params.AllowedTools,
		UnlimitedTurns:  true,
		Model:           subagentModel(params),
		ParentToolUseID: parentTool,
		TaskID:          params.TaskID,
	})
	if err != nil {
		emitAgentProgress(uctx, AgentProgressEvent{
			Kind:            "failed",
			AgentID:         string(id),
			ParentAgentID:   parentID,
			ParentToolUseID: parentTool,
			TaskID:          params.TaskID,
			Description:     params.Description,
			Output:          err.Error(),
			IsError:         true,
		})
		return "", fmt.Errorf("spawn task %s: %w", label, err)
	}
	result, err := t.coordinator.Wait(ctx, id)
	if err != nil {
		emitAgentProgress(uctx, AgentProgressEvent{
			Kind:            "failed",
			AgentID:         string(id),
			ParentAgentID:   parentID,
			ParentToolUseID: parentTool,
			TaskID:          params.TaskID,
			Description:     params.Description,
			Output:          err.Error(),
			IsError:         true,
		})
		return "", fmt.Errorf("wait task %s: %w", label, err)
	}
	if result.Error != "" {
		emitAgentProgress(uctx, AgentProgressEvent{
			Kind:            "failed",
			AgentID:         string(id),
			ParentAgentID:   parentID,
			ParentToolUseID: parentTool,
			TaskID:          params.TaskID,
			Description:     params.Description,
			Output:          result.Error,
			IsError:         true,
		})
		return "", fmt.Errorf("task %s failed: %s", label, result.Error)
	}
	if err := ctx.Err(); err != nil {
		emitAgentProgress(uctx, AgentProgressEvent{
			Kind:            "cancelled",
			AgentID:         string(id),
			ParentAgentID:   parentID,
			ParentToolUseID: parentTool,
			TaskID:          params.TaskID,
			Description:     params.Description,
			Output:          err.Error(),
			IsError:         true,
		})
		return "", fmt.Errorf("task %s canceled: %w", label, err)
	}
	emitAgentProgress(uctx, AgentProgressEvent{
		Kind:            "completed",
		AgentID:         string(id),
		ParentAgentID:   parentID,
		ParentToolUseID: parentTool,
		TaskID:          params.TaskID,
		Description:     params.Description,
		Output:          result.Output,
	})
	return result.Output, nil
}

func emitAgentProgress(uctx *UseContext, event AgentProgressEvent) {
	if uctx == nil {
		return
	}
	if uctx.OnAgentProgress != nil {
		uctx.OnAgentProgress(event)
	}
	if uctx.Status == nil {
		return
	}
	label := event.Description
	if label == "" {
		label = event.TaskID
	}
	switch event.Kind {
	case "started":
		uctx.Status(fmt.Sprintf("subagent parent_tool_use_id=%s task_id=%s agent_id=%s started: %s", event.ParentToolUseID, event.TaskID, event.AgentID, label))
	case "retry":
		uctx.Status(fmt.Sprintf("subagent parent_tool_use_id=%s task_id=%s %s", event.ParentToolUseID, event.TaskID, strings.TrimSpace(event.Output)))
	case "tool_start":
		uctx.Status(fmt.Sprintf("subagent %s running %s", firstNonEmpty(event.TaskID, event.AgentID), event.ToolName))
	case "tool_done":
		state := "done"
		if event.IsError {
			state = "failed"
		}
		uctx.Status(fmt.Sprintf("subagent %s %s %s", firstNonEmpty(event.TaskID, event.AgentID), event.ToolName, state))
	case "completed":
		uctx.Status(fmt.Sprintf("subagent parent_tool_use_id=%s task_id=%s agent_id=%s completed: %s", event.ParentToolUseID, event.TaskID, event.AgentID, label))
	case "failed", "cancelled":
		uctx.Status(fmt.Sprintf("subagent parent_tool_use_id=%s task_id=%s agent_id=%s %s: %s", event.ParentToolUseID, event.TaskID, event.AgentID, event.Kind, strings.TrimSpace(event.Output)))
	}
}

func parentToolUseID(uctx *UseContext) string {
	if uctx == nil {
		return ""
	}
	return strings.TrimSpace(uctx.MessageID)
}

func parentAgentID(uctx *UseContext) string {
	if uctx == nil {
		return ""
	}
	return strings.TrimSpace(uctx.AgentID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func subagentModel(params SubagentParams) string {
	model := strings.TrimSpace(params.Model)
	if strings.EqualFold(model, "fast") {
		return strings.TrimSpace(params.FastModel)
	}
	if model != "" {
		return model
	}
	if strings.EqualFold(params.Difficulty, "easy") || strings.EqualFold(params.Difficulty, "fast") {
		return strings.TrimSpace(params.FastModel)
	}
	return ""
}

// waitForTaskRetry waits before another Subagent attempt (shared with Task legacy helpers).
func waitForTaskRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
