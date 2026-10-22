package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestPlanModeBlocksAskUserBeforeDialog(t *testing.T) {
	for _, name := range []string{tool.AskUserToolName, "AskUserQuestion"} {
		t.Run(name, func(t *testing.T) {
			registry := tool.NewRegistry()
			registry.Register(tool.NewAskUserTool())
			permissions := permission.NewServiceWithConfig(permission.Config{
				Mode:  permission.ModePlan,
				Allow: []string{tool.AskUserToolName},
			})
			permissions.AllowTool(tool.AskUserToolName)
			executor := NewToolExecutorWithPermissions(registry, nil, permissions)
			invoked, asked := false, false
			uctx := &tool.UseContext{AskUser: func(context.Context, tool.AskUserParams) (map[string]string, error) {
				asked = true
				return map[string]string{"Proceed?": "Yes"}, nil
			}}
			out := executor.Execute(t.Context(), ToolCall{
				Name:  name,
				Input: json.RawMessage(`{"questions":[{"question":"Proceed?","options":[{"label":"Yes","description":"Proceed"},{"label":"No","description":"Stop"}]}]}`),
			}, ToolEnv{UseContext: uctx, OnInvokeStart: func() { invoked = true }})
			if !out.IsError || out.Content == nil || !strings.Contains(out.Content.Text, "AskUser is disabled in plan mode") {
				t.Fatalf("AskUser should be denied before invocation: %+v", out)
			}
			if invoked || asked {
				t.Fatal("plan must not invoke AskUser or open a dialog")
			}
		})
	}
}
