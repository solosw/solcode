package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
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
	taskToolTimeout    = 30 * time.Minute
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

	if err := selected.ValidateInput(ctx, input); err != nil {
		content := tool.ErrorResult("invalid parameters: " + err.Error())
		return ToolResult{Content: content, IsError: true}
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
			content = hook.ApplyModifiedResult(content, result.ModifiedResult)
		}
	}
	return ToolResult{Content: content, IsError: content.IsError}
}
