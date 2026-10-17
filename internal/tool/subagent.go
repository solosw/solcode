package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/agent"
)

const SubagentToolName = "Subagent"

// SubagentParams runs a single child agent. Task orchestrates multiple of these.
type SubagentParams struct {
	AgentID                 string `json:"agent_id,omitempty"`
	ForkContext             string `json:"fork_context,omitempty"`
	ForkLastN               int    `json:"fork_last_n,omitempty"`
	AcknowledgeInterruption bool   `json:"acknowledge_interruption,omitempty"`
	resume                  bool
	executionID             string
	runID                   string
	attempt                 int
	Description             string   `json:"description"`
	Prompt                  string   `json:"prompt"`
	AllowedTools            []string `json:"allowed_tools,omitempty"`
	Model                   string   `json:"model,omitempty"`
	Difficulty              string   `json:"difficulty,omitempty"`
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
expected return value.

allowed_tools is a RUNTIME permission on the child executor only. It does not
change the tools schema sent to the model: every agent (main and sub) shares
the same core tools prefix for prompt-cache stability. Disallowed tools still
appear in the schema but fail at invoke time if called. For BatchTool operations, list permitted child tool names (e.g. View/Grep) in allowed_tools; BatchTool alone does not authorize its children.`
}

func (t *subagentTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": agentReuseSchema(map[string]any{
			"description": map[string]any{"type": "string", "description": "Short label for this sub-agent"},
			"prompt":      map[string]any{"type": "string", "description": "Detailed prompt for the sub-agent"},
			"allowed_tools": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Runtime allowlist for this sub-agent's executor. " +
					"Does not change the shared tools schema prefix (core tools stay on the wire for prompt cache). " +
					"Empty means unrestricted execution (subject to permission mode).",
			},
			"model":      map[string]any{"type": "string", "description": "Optional explicit model or 'fast'"},
			"difficulty": map[string]any{"type": "string", "enum": []string{"easy", "medium", "hard"}, "description": "Use easy for the configured fast model"},
			"task_id":    map[string]any{"type": "string", "description": "Optional orchestration id from Task"},
		}),
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
	if uctx != nil && uctx.AgentRole != "" && uctx.AgentRole != "main" {
		return ErrorResult("nested subagents are not allowed"), nil
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

	params.AgentID = strings.TrimSpace(params.AgentID)
	params.resume = params.AgentID != ""
	if !params.resume {
		params.AgentID = agent.NewID("agent")
	}
	params.executionID = agent.NewID("execution")
	output, err := t.runWithRetry(ctx, uctx, &params)
	result := Result(output)
	if err != nil {
		result = ErrorResult(err.Error())
	}
	result.AgentID = params.AgentID
	result.RunID = params.runID
	return result, nil
}

func (t *subagentTool) runWithRetry(ctx context.Context, uctx *UseContext, params *SubagentParams) (string, error) {
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
		params.attempt = attempt + 1
		params.runID = agent.NewID("run")
		output, err := t.runAttempt(ctx, uctx, *params)
		params.resume = true
		if err == nil {
			return output, nil
		}
		var permanent *subagentNoRetry
		if ctx.Err() != nil || errors.As(err, &permanent) {
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
	id := agent.AgentID(params.AgentID)
	emitAgentProgress(uctx, AgentProgressEvent{
		Kind:            "started",
		AgentID:         string(id),
		ParentAgentID:   parentID,
		ParentToolUseID: parentTool,
		TaskID:          params.TaskID,
		Description:     params.Description,
	})
	start := t.coordinator.Spawn
	if params.resume {
		start = t.coordinator.Resume
	}
	parentSession := ""
	parentContext := agent.Snapshot{}
	if uctx != nil {
		parentSession = uctx.SessionID
		parentContext = uctx.ParentContext
	}
	_, err := start(ctx, agent.AgentConfig{
		ParentSessionID: parentSession, RunID: params.runID, ExecutionID: params.executionID, Attempt: params.attempt,
		ForkContext: params.ForkContext, ForkLastN: params.ForkLastN, ParentContext: parentContext, AcknowledgeInterruption: params.AcknowledgeInterruption,
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
		return "", &subagentNoRetry{fmt.Errorf("start task %s (agent_id=%s): %w", label, id, err)}
	}
	result, err := t.coordinator.WaitRun(ctx, id, params.runID)
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
		failure := fmt.Errorf("task %s failed: %s", label, result.Error)
		if result.NoRetry {
			return "", &subagentNoRetry{failure}
		}
		return "", failure
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

type subagentNoRetry struct{ error }

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
