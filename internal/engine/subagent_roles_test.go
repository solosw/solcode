package engine

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestTaskRoleAndParentCeilingsReachExecutor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name         string
		agentType    string
		parentTools  []string
		wantMutation int
	}{
		{"explorer ceiling", "explorer", nil, 0},
		{"reviewer ceiling", "reviewer", nil, 0},
		{"parent ceiling on worker", "worker", []string{"Task", "View"}, 0},
		{"worker permitted", "worker", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewRegistry()
			mutations := 0
			registry.Register(&backgroundTestTool{name: "Mutate", run: func(context.Context) *tool.ContentBlock { mutations++; return tool.Result("changed") }})
			client := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				switch index {
				case 0:
					return backgroundMessage(t, backgroundCall("delegate", "Task", map[string]any{"action": "run", "agent_type": tc.agentType, "description": "bounded work", "prompt": "work"})), nil
				case 1:
					if strings.Contains(req.System, "You are Worker") || strings.Contains(req.System, "You are Explorer") || strings.Contains(req.System, "You are Reviewer") {
						t.Error("role rewrote stable system prefix")
					}
					var messages strings.Builder
					for _, msg := range req.Messages {
						for _, block := range msg.Content {
							if block.OfText != nil {
								messages.WriteString(block.OfText.Text)
							}
						}
					}
					if !strings.Contains(messages.String(), `kind="agent-role"`) {
						t.Error("missing role overlay")
					}
					return backgroundMessage(t, backgroundCall("try-mutate", "Mutate", map[string]any{})), nil
				case 2:
					results := backgroundResultTexts(req.Messages, "try-mutate")
					if len(results) != 1 {
						t.Errorf("mutation results: %v", results)
					}
					return backgroundMessage(t), nil
				default:
					return backgroundMessage(t), nil
				}
			}}
			eng := NewEngine(Config{Client: client, Tools: registry, ModelName: "test", MaxTurns: 4, Permissions: permission.NewServiceWithConfig(permission.Config{Mode: permission.ModeBypass, BypassSubagents: true})})
			coordinator := agent.NewCoordinator(eng)
			registry.Register(tool.NewTaskTool(coordinator))
			result := eng.Run(t.Context(), agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, WorkDir: t.TempDir(), Prompt: "Task", AllowedTools: tc.parentTools, MaxTurns: 4})
			if result.Error != "" || mutations != tc.wantMutation || len(client.requests) != 4 {
				t.Fatalf("result=%+v mutations=%d requests=%d", result, mutations, len(client.requests))
			}
			if client.requests[0].System != client.requests[1].System {
				t.Fatal("parent/child system prefix differs")
			}
		})
	}
}
