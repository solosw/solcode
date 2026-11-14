package engine

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
)

type midRunClient struct {
	creates      int
	lastMsgCount int
}

func (c *midRunClient) Create(ctx context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
	c.creates++
	c.lastMsgCount = len(req.Messages)
	// Empty content / no tool uses ends the agent loop.
	return &sdk.Message{}, nil
}

func TestMidRunCompactionBeforeCreate(t *testing.T) {
	var compactCalls int
	client := &midRunClient{}
	eng := NewEngine(Config{
		Client:           client,
		ModelName:        "test-model",
		MaxContextTokens: 12000,
		MaxTokens:        64,
		MaxTurns:         1,
		CompactMessages: func(ctx context.Context, messages []sdk.MessageParam) ([]sdk.MessageParam, error) {
			compactCalls++
			return []sdk.MessageParam{
				sdk.NewUserMessage(sdk.NewTextBlock("compacted-history")),
			}, nil
		},
	})

	history := make([]sdk.MessageParam, 0, 24)
	for i := 0; i < 12; i++ {
		history = append(history,
			sdk.NewUserMessage(sdk.NewTextBlock("user message padding content that uses many tokens for estimation")),
			sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("assistant message padding content that uses many tokens for estimation ", 100))),
		)
	}

	result := eng.RunWithHistory(context.Background(), RunRequest{
		AgentConfig: agent.AgentConfig{
			ID:      "main",
			Role:    agent.AgentRoleMain,
			WorkDir: t.TempDir(),
			Prompt:  "continue the task",
		},
		Messages: history,
	})
	if result.AgentResult.Error != "" {
		t.Logf("run finished with: %s", result.AgentResult.Error)
	}
	if compactCalls != 0 {
		t.Fatal("hard overflow should directly trim instead of calling the legacy compactor")
	}
	if client.creates == 0 {
		t.Fatal("expected Client.Create after mid-run compaction attempt")
	}
	// Compacted history (1) + new user prompt (1) should be far smaller than original.
	if client.lastMsgCount >= len(history) {
		t.Fatalf("Create should see compacted history, got %d messages", client.lastMsgCount)
	}
}
