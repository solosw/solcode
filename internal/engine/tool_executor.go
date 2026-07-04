package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/solosw/codeplus-agent/internal/hook"
	"github.com/solosw/codeplus-agent/internal/permission"
	"github.com/solosw/codeplus-agent/internal/tool"
)

type ToolCall struct {
	Name  string
	Input json.RawMessage
}

type ToolEnv struct {
	UseContext *tool.UseContext
}

type ToolResult struct {
	Content *tool.ContentBlock
	IsError bool
}

const (
	defaultToolTimeout = 2 * time.Minute
	taskToolTimeout    = 10 * time.Minute
)

type ToolExecutor struct {
	registry    *tool.Registry
	hooks       *hook.Runtime
	permissions *permission.Service
}

func NewToolExecutor(registry *tool.Registry, hooks *hook.Runtime) *ToolExecutor {
	return &ToolExecutor{registry: registry, hooks: hooks}
}

func NewToolExecutorWithPermissions(registry *tool.Registry, hooks *hook.Runtime, permissions *permission.Service) *ToolExecutor {
	return &ToolExecutor{registry: registry, hooks: hooks, permissions: permissions}
}

func timeoutForTool(selected tool.Tool) time.Duration {
	if selected == nil {
		return defaultToolTimeout
	}
	if selected.Name() == tool.TaskToolName {
		return taskToolTimeout
	}
	return defaultToolTimeout
}

func (x *ToolExecutor) Execute(ctx context.Context, call ToolCall, env ToolEnv) ToolResult {
	selected := x.registry.Find(call.Name)
	if selected == nil {
		content := tool.ErrorResult(fmt.Sprintf("tool not found: %s", call.Name))
		return ToolResult{Content: content, IsError: true}
	}

	input := call.Input
	if x.hooks != nil {
		result, err := x.hooks.Run(ctx, hook.Event{
			Name:      hook.EventPreToolUse,
			WorkDir:   env.UseContext.WorkDir,
			ToolName:  call.Name,
			ToolInput: input,
		})
		if err != nil {
			content := tool.ErrorResult("pre-tool hook failed: " + err.Error())
			return ToolResult{Content: content, IsError: true}
		}
		if result.Decision == hook.DecisionBlock {
			content := tool.ErrorResult(result.Message)
			return ToolResult{Content: content, IsError: true}
		}
		if result.ModifiedInput != nil {
			input = result.ModifiedInput
		}
	}

	if x.permissions != nil {
		decision := x.permissions.Check(selected, input)
		if !decision.Allowed {
			content := tool.ErrorResult(decision.Reason)
			return ToolResult{Content: content, IsError: true}
		}
	}

	toolCtx, cancel := context.WithTimeout(ctx, timeoutForTool(selected))
	defer cancel()
	content, err := selected.Invoke(toolCtx, env.UseContext, input)
	if err != nil {
		switch {
		case toolCtx.Err() == context.Canceled:
			content = tool.ErrorResult("tool canceled")
		case toolCtx.Err() == context.DeadlineExceeded:
			content = tool.ErrorResult(fmt.Sprintf("tool timed out after %s", timeoutForTool(selected).Round(time.Second)))
		default:
			content = tool.ErrorResult(err.Error())
		}
	}
	if content == nil {
		content = tool.ErrorResult("tool returned nil result")
	}
	if x.hooks != nil {
		result, err := x.hooks.Run(ctx, hook.Event{
			Name:       hook.EventPostToolUse,
			WorkDir:    env.UseContext.WorkDir,
			ToolName:   call.Name,
			ToolInput:  input,
			ToolResult: content,
		})
		if err != nil {
			postHookError := tool.ErrorResult("post-tool hook failed: " + err.Error())
			return ToolResult{Content: postHookError, IsError: true}
		}
		if result.Decision == hook.DecisionBlock {
			blocked := tool.ErrorResult(result.Message)
			return ToolResult{Content: blocked, IsError: true}
		}
		if result.ModifiedResult != nil {
			if modified, ok := result.ModifiedResult.(map[string]any); ok {
				if text, ok := modified["text"].(string); ok {
					content = tool.Result(text)
				}
			}
		}
	}
	return ToolResult{Content: content, IsError: content.IsError}
}
