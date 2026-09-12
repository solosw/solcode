package engine

import (
	"context"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
)

type usageClient struct {
	usage sdk.Usage
}

func (c *usageClient) Create(ctx context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
	return &sdk.Message{Usage: c.usage}, nil
}

func TestOnUsageReportsTaskBillingWithoutOccupancy(t *testing.T) {
	var got []Usage
	client := &usageClient{usage: sdk.Usage{
		InputTokens:              11,
		OutputTokens:             3,
		CacheCreationInputTokens: 5,
		CacheReadInputTokens:     7,
	}}
	eng := NewEngine(Config{
		Client:           client,
		ModelName:        "test-model",
		MaxContextTokens: 100_000,
		MaxTokens:        64,
		MaxTurns:         1,
		OnUsage: func(u Usage) {
			got = append(got, u)
		},
	})

	result := eng.RunWithHistory(context.Background(), RunRequest{
		AgentConfig: agent.AgentConfig{
			ID:      "task-1",
			Role:    agent.AgentRoleTask,
			WorkDir: t.TempDir(),
			Prompt:  "do work",
		},
	})
	if result.AgentResult.Error != "" {
		t.Fatalf("run error: %s", result.AgentResult.Error)
	}
	if len(got) != 1 {
		t.Fatalf("OnUsage calls = %d, want 1", len(got))
	}
	u := got[0]
	if u.EstimatedContextTokens != 0 {
		t.Fatalf("task EstimatedContextTokens = %d, want 0", u.EstimatedContextTokens)
	}
	if u.InputTokens != 11 || u.OutputTokens != 3 || u.CacheCreationInputTokens != 5 || u.CacheReadInputTokens != 7 {
		t.Fatalf("billing usage = %+v", u)
	}
	if u.MaxContextTokens != 100_000 {
		t.Fatalf("MaxContextTokens = %d", u.MaxContextTokens)
	}
}

func TestOnUsageReportsMainOccupancyAndBilling(t *testing.T) {
	var got []Usage
	client := &usageClient{usage: sdk.Usage{
		InputTokens:              20,
		OutputTokens:             4,
		CacheCreationInputTokens: 1,
		CacheReadInputTokens:     2,
	}}
	eng := NewEngine(Config{
		Client:           client,
		ModelName:        "test-model",
		MaxContextTokens: 200_000,
		MaxTokens:        64,
		MaxTurns:         1,
		OnUsage: func(u Usage) {
			got = append(got, u)
		},
	})

	result := eng.RunWithHistory(context.Background(), RunRequest{
		AgentConfig: agent.AgentConfig{
			ID:      "main",
			Role:    agent.AgentRoleMain,
			WorkDir: t.TempDir(),
			Prompt:  "hello",
		},
	})
	if result.AgentResult.Error != "" {
		t.Fatalf("run error: %s", result.AgentResult.Error)
	}
	if len(got) != 1 {
		t.Fatalf("OnUsage calls = %d, want 1", len(got))
	}
	u := got[0]
	if u.EstimatedContextTokens <= 0 {
		t.Fatalf("main EstimatedContextTokens = %d, want > 0", u.EstimatedContextTokens)
	}
	if u.InputTokens != 20 || u.CacheReadInputTokens != 2 {
		t.Fatalf("billing usage = %+v", u)
	}
}
