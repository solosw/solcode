package engine

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
)

func TestPersistentChildContextLimitDoesNotChangeParent(t *testing.T) {
	for _, resolve := range []bool{false, true} {
		client := &persistentClient{respond: func(_ int, _ cpanthropic.MessageRequest) (*sdk.Message, error) { return backgroundMessage(t), nil }}
		cfg := Config{Client: client, ModelName: "main", MaxContextTokens: 200000, MaxTurns: 1}
		if resolve {
			cfg.ModelContextLimit = func(model string) int64 {
				if model != "small-child" {
					t.Errorf("wrong model: %s", model)
				}
				return 1
			}
		}
		eng := NewEngine(cfg)
		child := agent.AgentConfig{ID: "small", Role: agent.AgentRoleTask, Model: "small-child", Prompt: "work", MaxTurns: 1}
		if !resolve {
			child.MaxContextTokens = 1
		}
		result := eng.RunSession(t.Context(), agent.SessionRequest{Config: child, Save: func(agent.Checkpoint) error { return nil }})
		if result.Error == "" || !strings.Contains(result.Error, "context") || len(client.requests) != 0 {
			t.Fatalf("child did not enforce its own capacity: %+v", result)
		}
		if eng.config.MaxContextTokens != 200000 {
			t.Fatal("child changed the shared engine capacity")
		}
		child.ID = "large"
		child.Model = "large-child"
		child.MaxContextTokens = 200000
		result = eng.RunSession(t.Context(), agent.SessionRequest{Config: child, Save: func(agent.Checkpoint) error { return nil }})
		if result.Error != "" || len(client.requests) != 1 {
			t.Fatalf("sibling inherited another child capacity: %+v", result)
		}
	}
}
