package engine

import (
	"context"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestEngineOnlyExposesTaskInDelegatingMainAgent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     permission.Mode
		role     agent.AgentRole
		wantTask bool
	}{
		{"auto", permission.ModeAuto, agent.AgentRoleMain, false},
		{"plan", permission.ModePlan, agent.AgentRoleMain, false},
		{"bypass", permission.ModeBypass, agent.AgentRoleMain, true},
		{"goal", permission.ModeGoal, agent.AgentRoleMain, false},
		{"ultra", permission.ModeUltra, agent.AgentRoleMain, true},
		{"legacy ultra", "ultrl", agent.AgentRoleMain, true},
		{"task agent", permission.ModeUltra, agent.AgentRoleTask, false},
		{"sub agent", permission.ModeUltra, agent.AgentRoleSub, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewRegistry()
			registry.Register(tool.NewTaskTool(nil), tool.NewSubagentTool(nil), tool.NewModeSwitchTool(nil))
			client := &prefixCaptureClient{}
			eng := NewEngine(Config{Client: client, Tools: registry, ModelName: "test", MaxTurns: 1, Permissions: permission.NewService(tc.mode)})
			result := eng.Run(context.Background(), agent.AgentConfig{ID: "test", Role: tc.role, Prompt: "Task", MaxTurns: 1, WorkDir: t.TempDir()})
			if result.Error != "" || len(client.tools) != 1 {
				t.Fatalf("result = %+v, requests = %d", result, len(client.tools))
			}
			var taskVisible bool
			for _, name := range client.tools[0] {
				taskVisible = taskVisible || name == tool.TaskToolName
				if name == tool.SubagentToolName {
					t.Fatal("Subagent is internal and should never be exposed directly")
				}
			}
			if taskVisible != tc.wantTask {
				t.Fatalf("Task visibility = %v, want %v; tools = %v", taskVisible, tc.wantTask, client.tools[0])
			}
		})
	}
}
