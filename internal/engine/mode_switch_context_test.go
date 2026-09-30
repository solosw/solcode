package engine

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
)

type modeSwitchClient struct {
	systems  []string
	messages [][]sdk.MessageParam
	calls    int
	mode     *permission.Service
}

func (c *modeSwitchClient) Create(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
	c.systems = append(c.systems, req.System)
	c.messages = append(c.messages, req.Messages)
	c.calls++
	if c.calls == 1 {
		c.mode.SetMode(permission.ModePlan)
		return &sdk.Message{}, nil
	}
	return &sdk.Message{}, nil
}

func TestEngineRefreshesPlanContextAfterModeSwitch(t *testing.T) {
	permissions := permission.NewService(permission.ModeAuto)
	client := &modeSwitchClient{mode: permissions}
	engine := NewEngine(Config{Client: client, Permissions: permissions, MaxTurns: 1, ModelName: "test"})

	first := engine.Run(context.Background(), agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, Prompt: "first", MaxTurns: 1})
	if first.Error != "" {
		t.Fatalf("first Run error = %q", first.Error)
	}
	second := engine.Run(context.Background(), agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, Prompt: "second", MaxTurns: 1})
	if second.Error != "" {
		t.Fatalf("second Run error = %q", second.Error)
	}
	if len(client.systems) != 2 {
		t.Fatalf("request count = %d, want 2", len(client.systems))
	}
	if client.systems[0] != client.systems[1] {
		t.Fatalf("system prefix changed across mode switch\nfirst:\n%s\nsecond:\n%s", client.systems[0], client.systems[1])
	}
	// 精简的只读规则常驻 system prompt，两次请求都必须完整一致地包含。
	if !strings.Contains(client.systems[0], permission.PlanModeShortInstructions) ||
		!strings.Contains(client.systems[1], permission.PlanModeShortInstructions) {
		t.Fatal("compact plan-mode rules missing from the stable system prefix")
	}
	// 详细的 ACTIVE 指令（角色 + 输出格式）不能进 system prompt。
	if strings.Contains(client.systems[0], "(ACTIVE)") || strings.Contains(client.systems[1], "(ACTIVE)") {
		t.Fatal("active plan instructions leaked into the stable system prefix")
	}
	if messagesContain(client.messages[0], "(ACTIVE)") {
		t.Fatal("first request unexpectedly had active plan instructions")
	}
	if !messagesContain(client.messages[1], "(ACTIVE)") {
		t.Fatalf("second request did not refresh dynamic plan instructions: %+v", client.messages[1])
	}
}

func messagesContain(messages []sdk.MessageParam, text string) bool {
	for _, message := range messages {
		for _, block := range message.Content {
			if block.OfText != nil && strings.Contains(block.OfText.Text, text) {
				return true
			}
		}
	}
	return false
}
