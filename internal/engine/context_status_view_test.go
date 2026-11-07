package engine

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestContextStatusViews(t *testing.T) {
	for _, view := range []string{"overview", "ranges", "messages", "invalid"} {
		t.Run(view, func(t *testing.T) {
			reg := tool.NewRegistry()
			reg.Register(tool.ContextTools(nil, nil)...)
			client := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				if index == 0 {
					return backgroundMessage(t, backgroundCall("status", "acp_status", map[string]any{"view": view, "limit": 2})), nil
				}
				texts := backgroundResultTexts(req.Messages, "status")
				if len(texts) != 1 {
					t.Fatal(texts)
				}
				if view == "invalid" {
					if !strings.Contains(texts[0], "unknown status view") {
						t.Fatal(texts)
					}
				} else {
					want := "compressible_ranges"
					if view == "messages" {
						want = "safe_start"
					}
					if !strings.Contains(texts[0], want) || !strings.Contains(texts[0], "revision") {
						t.Fatal(texts)
					}
				}
				return backgroundMessage(t), nil
			}}
			e := NewEngine(Config{Client: client, Tools: reg, Permissions: permission.NewService(permission.ModeBypass), MaxTurns: 2})
			r := e.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: "current task"}, Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("old task")), sdk.NewAssistantMessage(sdk.NewTextBlock("completed work"))}})
			if r.AgentResult.Error != "" {
				t.Fatal(r.AgentResult.Error)
			}
		})
	}
}
