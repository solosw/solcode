package tool_test

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/tokenest"
	"github.com/solosw/solcode/internal/tool"
)

func TestTaskDefaultInheritanceUsesConfiguredChildCapacity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, action := range []string{"run", "spawn", "graph"} {
		t.Run(action, func(t *testing.T) {
			runner := &reusableRunner{}
			coordinator := agent.NewCoordinator(runner)
			defer coordinator.Close()
			task := tool.NewTaskTool(coordinator)
			parent := agent.Snapshot{}
			for i := 0; i < 50; i++ {
				parent.Messages = append(parent.Messages, sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("parent context ", 40))))
			}
			uctx := &tool.UseContext{WorkDir: t.TempDir(), AgentID: "main", SessionID: "parent-session", PermissionMode: "bypass", SubagentModel: "fast", FastModel: "configured-child", ParentContext: parent, ModelContextLimit: func(model string) int64 {
				if model != "configured-child" {
					t.Errorf("capacity resolved before model selection: %s", model)
				}
				return 2000
			}}
			input := map[string]any{"action": action, "description": "child", "prompt": "own work"}
			if action == "graph" {
				input = map[string]any{"tasks": []map[string]any{{"id": "first", "description": "graph child", "prompt": "own work"}}}
			}
			result := invokeTask(t, task, uctx, input)
			requireTask(t, result)
			if len(runner.configs) != 1 {
				t.Fatalf("runs=%d", len(runner.configs))
			}
			cfg := runner.configs[0]
			if cfg.MaxContextTokens != 2000 || cfg.ForkContext != "full" {
				t.Fatalf("wrong context policy: %+v", cfg)
			}
			history, err := coordinator.History(t.Context(), cfg.ID, agent.Scope{ParentID: "main", ParentSessionID: "parent-session", WorkDir: uctx.WorkDir})
			if err != nil {
				t.Fatal(err)
			}
			if len(history.Messages) < 3 || len(history.Messages) >= len(parent.Messages) {
				t.Fatalf("unexpected inherited messages: %d", len(history.Messages))
			}
			if tokens := tokenest.Messages(history.Messages); tokens > 850 {
				t.Fatalf("used parent capacity rather than child capacity: %d", tokens)
			}
			// Resume must retain child history and must not inherit a new parent snapshot.
			uctx.ParentContext = agent.Snapshot{Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("NEW_PARENT_MUST_NOT_BE_FORKED"))}}
			resumed := invokeTask(t, task, uctx, map[string]any{"action": "resume", "agent_id": cfg.ID, "prompt": "follow-up"})
			requireTask(t, resumed)
			history, err = coordinator.History(t.Context(), cfg.ID, agent.Scope{ParentID: "main", ParentSessionID: "parent-session", WorkDir: uctx.WorkDir})
			if err != nil || strings.Contains(tokenest.Transcript(history.Messages), "NEW_PARENT_MUST_NOT_BE_FORKED") {
				t.Fatal("resume forked the parent again")
			}
		})
	}
}
