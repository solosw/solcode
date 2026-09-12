package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
	// bashToolTimeout is slightly above Bash MaxTimeout (24h) so the executor
	// does not cancel a long auto-wait before the job's own lifetime ends.
	bashToolTimeout = 24*time.Hour + 30*time.Second
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
	switch selected.Name() {
	case tool.TaskToolName, tool.SubagentToolName:
		return taskToolTimeout
	case tool.WaitToolName:
		// Wait is internal (not model-visible); same ceiling as Bash auto-wait.
		return bashToolTimeout
	case tool.BashToolName:
		// Foreground short runs and long auto-waits share MaxTimeout (24h).
		return bashToolTimeout
	default:
		return defaultToolTimeout
	}
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

	captureCheckpointBeforeInvoke(ctx, selected, input, env.UseContext)
	beforeFP := captureFingerprintBeforeInvoke(selected, env.UseContext)

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
	captureFingerprintAfterInvoke(selected, env.UseContext, beforeFP)
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

func captureCheckpointBeforeInvoke(ctx context.Context, selected tool.Tool, input json.RawMessage, uctx *tool.UseContext) {
	if selected == nil || uctx == nil || uctx.CaptureCheckpoint == nil {
		return
	}
	if !tool.CheckpointableFileTool(selected.Name()) {
		return
	}
	for _, path := range tool.PathsForCheckpoint(selected.Name(), input) {
		abs := tool.ResolvePath(uctx, path)
		if strings.TrimSpace(abs) == "" {
			continue
		}
		data, err := tool.ReadTextFileContent(ctx, uctx, abs)
		if err != nil {
			if os.IsNotExist(err) {
				uctx.CaptureCheckpoint(abs, nil)
			}
			continue
		}
		text := string(data)
		uctx.CaptureCheckpoint(abs, &text)
	}
}

func captureFingerprintBeforeInvoke(selected tool.Tool, uctx *tool.UseContext) map[string]tool.FileFingerprint {
	if selected == nil || uctx == nil || uctx.CaptureCheckpoint == nil {
		return nil
	}
	if !tool.FingerprintCheckpointTool(selected.Name()) {
		return nil
	}
	before, err := tool.SnapshotWorkDir(uctx.WorkDir, tool.FingerprintOptions{}, true)
	if err != nil || before == nil {
		return map[string]tool.FileFingerprint{}
	}
	return before
}

func captureFingerprintAfterInvoke(selected tool.Tool, uctx *tool.UseContext, before map[string]tool.FileFingerprint) {
	if selected == nil || uctx == nil || uctx.CaptureCheckpoint == nil || before == nil {
		return
	}
	if !tool.FingerprintCheckpointTool(selected.Name()) {
		return
	}
	after, err := tool.SnapshotWorkDir(uctx.WorkDir, tool.FingerprintOptions{}, false)
	if err != nil {
		after = map[string]tool.FileFingerprint{}
	}
	for _, change := range tool.DiffFingerprints(before, after) {
		abs := tool.ResolvePath(uctx, change.Path)
		if strings.TrimSpace(abs) == "" {
			continue
		}
		uctx.CaptureCheckpoint(abs, change.Content)
	}
}
