package engine

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
)

type modelCaptureClient struct{ calls []cpanthropic.MessageRequest }

func (c *modelCaptureClient) Create(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
	c.calls = append(c.calls, req)
	return &sdk.Message{}, nil
}

func TestModelChangeDuringCompactionUsesConfirmedNextCall(t *testing.T) {
	old, next := &modelCaptureClient{}, &modelCaptureClient{}
	var eng *Engine
	eng = NewEngine(Config{Client: old, ModelName: "old", ModelKey: "provider-a", MaxContextTokens: 1, MaxTokens: 40, Effort: "high", MaxTurns: 1,
		CompactMessages: func(context.Context, []sdk.MessageParam) ([]sdk.MessageParam, error) {
			eng.UpdateMainModel(Config{Client: next, ModelName: "new", ModelKey: "provider-b", MaxContextTokens: 200000, MaxTokens: 80, Effort: "low"})
			return []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("retained summary"))}, nil
		},
	})
	result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, Prompt: "hello", WorkDir: t.TempDir()}})
	if result.AgentResult.Error != "" {
		t.Fatal(result.AgentResult.Error)
	}
	if len(old.calls) != 0 || len(next.calls) != 1 {
		t.Fatalf("wrong client: old=%d new=%d", len(old.calls), len(next.calls))
	}
	req := next.calls[0]
	if req.Model != "new" || req.Effort != "low" || req.MaxTokens != 80 {
		t.Fatalf("stale invocation config: %+v", req)
	}
	if len(req.Messages) == 0 {
		t.Fatal("lost conversation during live switch")
	}
}

func TestModelRefreshPreservesVisibleHistoryAndChildSnapshot(t *testing.T) {
	old, next := &modelCaptureClient{}, &modelCaptureClient{}
	root := NewEngine(Config{Client: old, ModelName: "old", ModelKey: "a", Effort: "high"})
	child := root.forRun()
	main := root.forRun()
	root.UpdateMainModel(Config{Client: next, ModelName: "new", ModelKey: "b", Effort: "low"})
	if child.config.Client != old || child.config.ModelName != "old" {
		t.Fatal("child snapshot was changed")
	}
	history := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("user text")),
		sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{OfThinking: &sdk.ThinkingBlockParam{Thinking: "private", Signature: "signed"}}, sdk.NewTextBlock("assistant text"), sdk.NewToolUseBlock("call-1", map[string]any{}, "test")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("call-1", "tool output", false)),
	}
	got := main.refreshMainModel(history)
	if main.config.Client != next || main.config.Effort != "low" {
		t.Fatal("main snapshot did not refresh")
	}
	if len(got) != 3 || len(got[1].Content) != 2 || got[1].Content[1].OfToolUse.ID != "call-1" {
		t.Fatalf("lost visible history: %+v", got)
	}
	if len(history[1].Content) != 3 {
		t.Fatal("refresh mutated the caller's history")
	}
	if strings.Contains(got[1].Content[0].OfText.Text, "private") {
		t.Fatal("private reasoning leaked")
	}
}

func TestModelRefreshRevokesOldReasoningAcrossRetryWithoutRemovingNewReasoning(t *testing.T) {
	root := NewEngine(Config{ModelName: "old", ModelKey: "a"})
	old := sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{OfThinking: &sdk.ThinkingBlockParam{Thinking: "old private", Signature: "old signed"}}, sdk.NewTextBlock("visible history"))
	root.UpdateMainModel(Config{ModelName: "new", ModelKey: "b"})
	first := root.forRun().refreshMainModel([]sdk.MessageParam{old})
	if len(first[0].Content) != 1 {
		t.Fatal("first request kept old reasoning")
	}
	// A transient error can restart the main agent with the original saved
	// history. Consuming the one-time reset flag must not re-enable old blocks.
	fresh := sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{OfThinking: &sdk.ThinkingBlockParam{Thinking: "new private", Signature: "new signed"}}, sdk.NewTextBlock("new answer"))
	retried := root.forRun().refreshMainModel([]sdk.MessageParam{old, fresh})
	if len(retried[0].Content) != 1 {
		t.Fatal("retry forwarded revoked model-specific reasoning")
	}
	if len(retried[1].Content) != 2 || retried[1].Content[0].OfThinking == nil {
		t.Fatal("retry removed current model's valid reasoning")
	}
}

func TestRestoredHistoryInvalidatesOpaqueReasoningEvenWithSameModelID(t *testing.T) {
	root := NewEngine(Config{ModelName: "same", ModelKey: "provider-b"})
	root.InvalidateReasoningHistory()
	restored := sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{OfThinking: &sdk.ThinkingBlockParam{Thinking: "unverified provider-a reasoning", Signature: "signed"}}, sdk.NewTextBlock("keep restored answer"))
	got := root.forRun().refreshMainModel([]sdk.MessageParam{restored})
	if len(got) != 1 || len(got[0].Content) != 1 || got[0].Content[0].OfText.Text != "keep restored answer" {
		t.Fatal("restored reasoning was trusted or visible history lost")
	}
}
