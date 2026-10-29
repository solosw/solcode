package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/tool"
)

type terminalRunnerFunc func(context.Context, *tool.UseContext, string, []string) (*tool.ContentBlock, error)

func (f terminalRunnerFunc) Run(ctx context.Context, uctx *tool.UseContext, command string, args []string) (*tool.ContentBlock, error) {
	return f(ctx, uctx, command, args)
}

func TestClientTerminalPreservesBackgroundScheduling(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var executions atomic.Int32
	runner := terminalRunnerFunc(func(ctx context.Context, uctx *tool.UseContext, command string, args []string) (*tool.ContentBlock, error) {
		executions.Add(1)
		if uctx.MessageID != "long" || uctx.RunID != "main-run" || command == "" || len(args) != 2 {
			t.Errorf("terminal identity=%+v command=%s args=%v", uctx, command, args)
		}
		select {
		case <-release:
			return tool.Result("client terminal completed"), nil
		case <-ctx.Done():
			return tool.ErrorResult(ctx.Err().Error()), nil
		}
	})
	registry := tool.NewRegistry()
	registry.Register(tool.NewBashTool(), &backgroundTestTool{name: tool.ViewToolName, run: func(context.Context) *tool.ContentBlock { return tool.Result("independent work") }})
	calls, completions := 0, 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		switch calls {
		case 1:
			return backgroundMessage(t, backgroundCall("long", tool.BashToolName, map[string]any{"command": "echo remote", "run_in_background": true})), nil
		case 2:
			texts := backgroundResultTexts(req.Messages, "long")
			if len(texts) != 1 || !strings.Contains(texts[0], "running_in_background") {
				t.Errorf("missing background ack: %v", texts)
			}
			return backgroundMessage(t, backgroundCall("independent", tool.ViewToolName, map[string]any{})), nil
		case 3:
			if texts := backgroundResultTexts(req.Messages, "independent"); len(texts) != 1 || texts[0] != "independent work" {
				t.Errorf("independent work did not complete: %v", texts)
			}
			unblock()
			return backgroundMessage(t), nil
		case 4:
			if !messagesContain(req.Messages, "client terminal completed") || !messagesContain(req.Messages, "status: completed") {
				t.Error("missing terminal completion notification")
			}
			return backgroundMessage(t), nil
		default:
			return nil, fmt.Errorf("unexpected model polling: %d", calls)
		}
	})
	eng := NewEngine(Config{Client: client, Tools: registry, TerminalRunner: runner, BackgroundTasksEnabled: true, BackgroundYieldAfter: time.Hour, MaxTurns: 8,
		OnToolDone: func(name, output string, isError bool, id string) {
			if id == "long" {
				completions++
				if isError || output != "client terminal completed" {
					t.Errorf("completion=%q error=%v", output, isError)
				}
			}
		},
	})
	result := eng.Run(ctx, agent.AgentConfig{ID: "main", RunID: "main-run", Role: agent.AgentRoleMain, Prompt: "work", WorkDir: t.TempDir()})
	if result.Error != "" || executions.Load() != 1 || calls != 4 || completions != 1 {
		t.Fatalf("result=%+v executions=%d requests=%d completions=%d", result, executions.Load(), calls, completions)
	}
}

func TestClientTerminalReachesSubagent(t *testing.T) {
	calls := 0
	runner := terminalRunnerFunc(func(ctx context.Context, uctx *tool.UseContext, command string, args []string) (*tool.ContentBlock, error) {
		calls++
		if uctx.AgentID != "child" || uctx.RunID != "child-run" || uctx.AgentRole != "task" || uctx.MessageID != "child-bash" {
			t.Errorf("child identity=%+v", uctx)
		}
		return tool.Result("child terminal output"), nil
	})
	registry := tool.NewRegistry()
	registry.Register(tool.NewBashTool())
	client := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		if index == 0 {
			return backgroundMessage(t, backgroundCall("child-bash", tool.BashToolName, map[string]any{"command": "echo child"})), nil
		}
		if texts := backgroundResultTexts(req.Messages, "child-bash"); len(texts) != 1 || texts[0] != "child terminal output" {
			t.Errorf("missing child tool result: %v", texts)
		}
		return backgroundMessage(t), nil
	}}
	eng := NewEngine(Config{Client: client, Tools: registry, TerminalRunner: runner, MaxTurns: 3})
	result := eng.Run(t.Context(), agent.AgentConfig{ID: "child", RunID: "child-run", ParentID: "main", Role: agent.AgentRoleTask, Prompt: "inspect", WorkDir: t.TempDir()})
	if result.Error != "" || calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
}
