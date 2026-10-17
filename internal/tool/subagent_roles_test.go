package tool_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
	"github.com/solosw/solcode/internal/tool"
)

func TestTaskRoleInstancesResumeAndPermissionCeiling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &reusableRunner{}
	storeDir, workDir := t.TempDir(), t.TempDir()
	c := agent.NewCoordinator(runner, agentsession.NewFileStore(storeDir))
	task := tool.NewTaskTool(c)
	uctx := &tool.UseContext{WorkDir: workDir, AgentID: "main", SessionID: "session", PermissionMode: "bypass", AllowedTools: []string{"View", "Write"}}
	first := invokeTask(t, task, uctx, map[string]any{"action": "spawn", "agent_type": "explorer", "description": "Analyze Session", "prompt": "find persistence"})
	requireTask(t, first)
	id := resultAgentID(first.Text)
	second := invokeTask(t, task, uctx, map[string]any{"action": "spawn", "agent_type": "explorer", "description": "Other Explorer", "prompt": "independent history"})
	requireTask(t, second)
	if id == "" || id == resultAgentID(second.Text) {
		t.Fatal("roles must not identify instances")
	}
	if cfg := runner.configs[0]; cfg.AgentType != "explorer" || cfg.RoleInstructions == "" || !reflect.DeepEqual(cfg.AllowedTools, []string{"View"}) {
		t.Fatalf("role/ceiling: %+v", cfg)
	}
	c = agent.NewCoordinator(runner, agentsession.NewFileStore(storeDir))
	task = tool.NewTaskTool(c)
	resumed := invokeTask(t, task, uctx, map[string]any{"action": "resume", "agent_id": id, "prompt": "trace child history", "allowed_tools": []string{"Write"}})
	requireTask(t, resumed)
	if resultAgentID(resumed.Text) != id {
		t.Fatal("resume replaced the instance")
	}
	cfg := runner.configs[2]
	if cfg.AgentType != "explorer" || cfg.Description != "Analyze Session" || !reflect.DeepEqual(cfg.AllowedTools, []string{"__no_tools__"}) {
		t.Fatalf("resume changed identity or expanded tools: %+v", cfg)
	}
	status := invokeTask(t, task, uctx, map[string]any{"action": "status", "agent_id": id})
	requireTask(t, status)
	if !strings.Contains(status.Text, `"agent_type":"explorer"`) {
		t.Fatalf("status has no type: %s", status.Text)
	}
	one := invokeTask(t, task, uctx, map[string]any{"action": "history", "agent_id": id})
	two := invokeTask(t, task, uctx, map[string]any{"action": "history", "agent_id": resultAgentID(second.Text)})
	requireTask(t, one)
	requireTask(t, two)
	if !strings.Contains(one.Text, "trace child history") || strings.Contains(two.Text, "trace child history") {
		t.Fatal("instance histories were mixed")
	}
	bad := invokeTask(t, task, uctx, map[string]any{"action": "resume", "agent_id": id, "agent_type": "worker", "prompt": "change role"})
	if !bad.IsError || !strings.Contains(bad.Text, "cannot change") {
		t.Fatalf("role mutation: %+v", bad)
	}
}

func TestTaskRoleOverridesAndBypassRestrictions(t *testing.T) {
	home, workDir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	roles := filepath.Join(workDir, ".solcode", "roles")
	if err := os.MkdirAll(roles, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"worker": "instructions = 'Project Worker'\nmodel = 'fast'\nallowed_tools = ['View', 'Write', 'Bash']\n",
		"custom": "instructions = 'Custom agent'\n",
	} {
		if err := os.WriteFile(filepath.Join(roles, name+".toml"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &reusableRunner{}
	task := tool.NewTaskTool(agent.NewCoordinator(runner))
	uctx := &tool.UseContext{WorkDir: workDir, PermissionMode: "bypass", FastModel: "fast-model", AllowedTools: []string{"View"}}
	spawned := invokeTask(t, task, uctx, map[string]any{"action": "spawn", "description": "default", "prompt": "implement"})
	requireTask(t, spawned)
	if cfg := runner.configs[0]; cfg.AgentType != "worker" || cfg.Model != "fast-model" || cfg.RoleInstructions != "Project Worker" || !reflect.DeepEqual(cfg.AllowedTools, []string{"View"}) {
		t.Fatalf("override escaped parent: %+v", cfg)
	}
	explicit := invokeTask(t, task, uctx, map[string]any{"description": "explicit", "prompt": "implement", "model": "explicit-model"})
	requireTask(t, explicit)
	if runner.configs[1].Model != "explicit-model" {
		t.Fatal("role model overrode explicit selection")
	}
	for _, name := range []string{"custom", "default", "../worker"} {
		result := invokeTask(t, task, uctx, map[string]any{"agent_type": name, "description": "invalid", "prompt": "work"})
		if !result.IsError {
			t.Fatalf("bypass accepted %s", name)
		}
	}
	uctx.PermissionMode = "ultra"
	custom := invokeTask(t, task, uctx, map[string]any{"agent_type": "custom", "description": "custom", "prompt": "work"})
	requireTask(t, custom)
	uctx.PermissionMode = "bypass"
	resumed := invokeTask(t, task, uctx, map[string]any{"action": "resume", "agent_id": resultAgentID(custom.Text), "prompt": "must reject stored custom type"})
	if !resumed.IsError || !strings.Contains(resumed.Text, "bypass only allows") {
		t.Fatalf("custom id bypassed restriction: %+v", resumed)
	}
	if len(runner.configs) != 3 {
		t.Fatalf("rejected requests started models: %d", len(runner.configs))
	}
}

func TestTaskRoleResumeReloadsConfigWithoutExpandingCeiling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &reusableRunner{}
	workDir := t.TempDir()
	roles := filepath.Join(workDir, ".solcode", "roles")
	if err := os.MkdirAll(roles, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(roles, "worker.toml")
	if err := os.WriteFile(path, []byte("model = 'first'\nallowed_tools = ['View']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	task := tool.NewTaskTool(agent.NewCoordinator(runner))
	uctx := &tool.UseContext{WorkDir: workDir, PermissionMode: "bypass"}
	first := invokeTask(t, task, uctx, map[string]any{"description": "worker", "prompt": "work"})
	requireTask(t, first)
	if err := os.WriteFile(path, []byte("model = 'second'\ninstructions = 'New behavior'\nallowed_tools = ['View', 'Write']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resumed := invokeTask(t, task, uctx, map[string]any{"action": "resume", "agent_id": resultAgentID(first.Text), "prompt": "continue"})
	requireTask(t, resumed)
	cfg := runner.configs[1]
	if cfg.Model != "second" || cfg.RoleInstructions != "New behavior" || !reflect.DeepEqual(cfg.AllowedTools, []string{"View"}) {
		t.Fatalf("resume role/ceiling: %+v", cfg)
	}
}

func TestTaskUntypedLegacyInstanceRequiresUltra(t *testing.T) {
	runner := &reusableRunner{}
	coordinator := agent.NewCoordinator(runner)
	workDir := t.TempDir()
	cfg := agent.AgentConfig{ID: "legacy-child", Role: agent.AgentRoleTask, WorkDir: workDir, Description: "legacy", Prompt: "first"}
	if _, err := coordinator.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Wait(t.Context(), cfg.ID); err != nil {
		t.Fatal(err)
	}
	task := tool.NewTaskTool(coordinator)
	uctx := &tool.UseContext{WorkDir: workDir, PermissionMode: "bypass"}
	result := invokeTask(t, task, uctx, map[string]any{"action": "resume", "agent_id": cfg.ID, "prompt": "continue"})
	if !result.IsError {
		t.Fatal("untyped legacy instance accepted in bypass")
	}
	uctx.PermissionMode = "ultra"
	requireTask(t, invokeTask(t, task, uctx, map[string]any{"action": "resume", "agent_id": cfg.ID, "prompt": "continue"}))
	if runner.configs[1].AgentType != "" {
		t.Fatal("legacy instance silently changed type")
	}
}

func TestTaskGraphPropagatesAgentTypes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &reusableRunner{}
	task := tool.NewTaskTool(agent.NewCoordinator(runner))
	result := invokeTask(t, task, &tool.UseContext{WorkDir: t.TempDir()}, map[string]any{
		"agent_type": "reviewer", "execution_mode": "serial", "tasks": []map[string]any{
			{"id": "one", "description": "review", "prompt": "review"},
			{"id": "two", "agent_type": "explorer", "description": "explore", "prompt": "explore", "depends_on": []string{"one"}},
		},
	})
	requireTask(t, result)
	if len(runner.configs) != 2 || runner.configs[0].AgentType != "reviewer" || runner.configs[1].AgentType != "explorer" {
		t.Fatalf("types: %+v", runner.configs)
	}
}
