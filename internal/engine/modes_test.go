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
		name            string
		mode            permission.Mode
		role            agent.AgentRole
		wantTask        bool
		bypassSubagents bool
	}{
		{"auto", permission.ModeAuto, agent.AgentRoleMain, false, false},
		{"plan", permission.ModePlan, agent.AgentRoleMain, false, false},
		{"bypass default", permission.ModeBypass, agent.AgentRoleMain, false, false},
		{"bypass enabled", permission.ModeBypass, agent.AgentRoleMain, true, true},
		{"yolo enabled", "yolo", agent.AgentRoleMain, true, true},
		{"plan with bypass enabled", permission.ModePlan, agent.AgentRoleMain, false, true},
		{"bypass task agent", permission.ModeBypass, agent.AgentRoleTask, false, true},
		{"bypass sub agent", permission.ModeBypass, agent.AgentRoleSub, false, true},
		{"goal", permission.ModeGoal, agent.AgentRoleMain, false, false},
		{"ultra", permission.ModeUltra, agent.AgentRoleMain, true, false},
		{"legacy ultra", "ultrl", agent.AgentRoleMain, true, false},
		{"task agent", permission.ModeUltra, agent.AgentRoleTask, false, false},
		{"sub agent", permission.ModeUltra, agent.AgentRoleSub, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewRegistry()
			registry.Register(tool.NewTaskTool(nil), tool.NewSubagentTool(nil), tool.NewModeSwitchTool(nil))
			client := &prefixCaptureClient{}
			eng := NewEngine(Config{Client: client, Tools: registry, ModelName: "test", MaxTurns: 1, Permissions: permission.NewServiceWithConfig(permission.Config{Mode: tc.mode, BypassSubagents: tc.bypassSubagents})})
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

func TestBypassSubagentToggleControlsLiveSchemasAndExecution(t *testing.T) {
	permissions := permission.NewService(permission.ModeBypass)
	registry := tool.NewRegistry()
	invocations := 0
	for _, name := range []string{tool.TaskToolName, tool.SubagentToolName} {
		registry.Register(&backgroundTestTool{name: name, run: func(context.Context) *tool.ContentBlock {
			invocations++
			return tool.Result("ran")
		}})
		permissions.AllowTool(name)
	}
	client := &prefixCaptureClient{}
	eng := NewEngine(Config{Client: client, Tools: registry, ModelName: "test", MaxTurns: 1, Permissions: permissions})
	executor := NewToolExecutorWithPermissions(registry, nil, permissions)
	workDir := t.TempDir()
	for index, enabled := range []bool{false, true, false} {
		permissions.SetBypassSubagents(enabled)
		result := eng.Run(t.Context(), agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, WorkDir: workDir, Prompt: "Task"})
		if result.Error != "" {
			t.Fatal(result.Error)
		}
		visible := false
		for _, name := range client.tools[index] {
			visible = visible || name == tool.TaskToolName
		}
		if visible != enabled {
			t.Fatalf("live schema ignored toggle: enabled=%v, tools=%v", enabled, client.tools[index])
		}
		for _, name := range []string{tool.TaskToolName, tool.SubagentToolName} {
			before := invocations
			out := executor.Execute(t.Context(), ToolCall{Name: name, Input: []byte(`{"agent_type":"worker"}`)}, ToolEnv{UseContext: &tool.UseContext{WorkDir: workDir, AgentID: "main", PermissionMode: "bypass"}})
			if out.IsError == enabled || (invocations > before) != enabled {
				t.Fatalf("%s ignored toggle: enabled=%v, output=%+v, invocations=%d", name, enabled, out, invocations-before)
			}
		}
	}
}
