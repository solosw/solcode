package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

type parentExecutorTestTool struct {
	timeoutTestTool
	invoke func(context.Context) (*tool.ContentBlock, error)
}

func (t parentExecutorTestTool) Invoke(ctx context.Context, _ *tool.UseContext, _ json.RawMessage) (*tool.ContentBlock, error) {
	return t.invoke(ctx)
}

func TestParentExecutorWaitHasNoDeadlineAndRemainsCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	reg := tool.NewRegistry()
	reg.Register(parentExecutorTestTool{timeoutTestTool: timeoutTestTool{name: tool.AskParentToolName}, invoke: func(ctx context.Context) (*tool.ContentBlock, error) {
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Error("parent wait has an executor deadline")
		}
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	executor := NewToolExecutor(reg, nil).WithAllowedTools([]string{tool.ViewToolName})
	done := make(chan ToolResult, 1)
	go func() {
		done <- executor.Execute(ctx, ToolCall{Name: tool.AskParentToolName, Input: json.RawMessage("{}")}, ToolEnv{})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("AskParent was blocked by the child allowlist")
	}
	cancel()
	select {
	case result := <-done:
		if !result.IsError || !strings.Contains(result.Content.Text, "canceled") {
			t.Fatalf("cancellation = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parent wait ignored cancellation")
	}
}

func TestParentExecutorStillChecksGlobalDenial(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(parentExecutorTestTool{timeoutTestTool: timeoutTestTool{name: tool.AskParentToolName}, invoke: func(context.Context) (*tool.ContentBlock, error) {
		t.Error("denied AskParent executed")
		return tool.Result("unexpected"), nil
	}})
	permissions := permission.NewService(permission.ModeBypass)
	permissions.DenyTool(tool.AskParentToolName)
	executor := NewToolExecutorWithPermissions(reg, nil, permissions).WithAllowedTools([]string{tool.ViewToolName})
	result := executor.Execute(t.Context(), ToolCall{Name: tool.AskParentToolName, Input: json.RawMessage("{}")}, ToolEnv{})
	if !result.IsError {
		t.Fatal("AskParent escaped global permission denial")
	}
}
