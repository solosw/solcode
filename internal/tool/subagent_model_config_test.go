package tool_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/tool"
)

func TestTaskModelSelectionIsHostOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct{ name, configured, fast, want string }{
		{"inherit", "", "host-fast", ""},
		{"configured", "child-model", "host-fast", "child-model"},
		{"configured fast", "fast", "host-fast", "host-fast"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &reusableRunner{}
			task := tool.NewTaskTool(agent.NewCoordinator(runner))
			uctx := &tool.UseContext{WorkDir: t.TempDir(), PermissionMode: "bypass", SubagentModel: tc.configured, FastModel: tc.fast}
			for _, action := range []string{"run", "spawn"} {
				result := invokeTask(t, task, uctx, map[string]any{"action": action, "description": "child", "prompt": "work", "model": "ai-model", "difficulty": "easy", "fast_model": "ai-fast"})
				requireTask(t, result)
				requireTask(t, invokeTask(t, task, uctx, map[string]any{"action": "wait", "agent_id": resultAgentID(result.Text)}))
			}
			graph := invokeTask(t, task, uctx, map[string]any{"fast_model": "ai-fast", "tasks": []map[string]any{{"id": "one", "description": "graph", "prompt": "work", "model": "fast", "difficulty": "easy"}}})
			requireTask(t, graph)
			for _, cfg := range runner.configs {
				if cfg.Model != tc.want {
					t.Errorf("model = %q, want %q", cfg.Model, tc.want)
				}
			}
			if len(runner.configs) != 3 {
				t.Fatalf("runs = %d", len(runner.configs))
			}
		})
	}
}

func TestSubagentSchemasHideModelSelection(t *testing.T) {
	for _, candidate := range []tool.Tool{tool.NewTaskTool(nil), tool.NewSubagentTool(nil)} {
		raw, err := json.Marshal(candidate.InputSchema())
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{`"model":`, `"difficulty":`, `"fast_model":`} {
			if bytes.Contains(raw, []byte(field)) {
				t.Errorf("%s exposes %s", candidate.Name(), field)
			}
		}
	}
}
