package acp

import (
	"encoding/json"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/engine"
	contextmessage "github.com/solosw/solcode/internal/message"
	"github.com/solosw/solcode/internal/session"
)

func TestUsageUpdateMatchesTUIContextOccupancy(t *testing.T) {
	// TUI footer ctx used/limit comes from EstimatedContextTokens / MaxContextTokens.
	u := usageUpdate(engine.Usage{
		EstimatedContextTokens:   12_500,
		InputTokens:              10_000, // session billing totals — must NOT become used
		OutputTokens:             500,
		CacheCreationInputTokens: 1_000,
		CacheReadInputTokens:     2_000,
		MaxContextTokens:         200_000,
	})
	if u == nil {
		t.Fatal("usageUpdate returned nil")
	}
	if u.Used != 12_500 {
		t.Fatalf("used = %d, want TUI EstimatedContextTokens 12500", u.Used)
	}
	if u.Size != 200_000 {
		t.Fatalf("size = %d, want TUI MaxContextTokens 200000", u.Size)
	}

	raw, err := json.Marshal(SessionUpdate{SessionUpdate: "usage_update", Usage: u})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["sessionUpdate"] != "usage_update" {
		t.Fatalf("sessionUpdate = %#v", payload["sessionUpdate"])
	}
	// claude-agent-acp flattens used/size onto the update — not under "usage".
	if _, ok := payload["usage"]; ok {
		t.Fatalf("usage must not be nested: %#v", payload)
	}
	if payload["used"] != float64(12_500) {
		t.Fatalf("used = %#v, want 12500", payload["used"])
	}
	if payload["size"] != float64(200_000) {
		t.Fatalf("size = %#v, want 200000", payload["size"])
	}
	if _, ok := payload["size"].(float64); !ok {
		t.Fatalf("size type = %T, want number", payload["size"])
	}
}

func TestUsageUpdateDoesNotUseBillingTotalsAsOccupancy(t *testing.T) {
	// Without an estimate, used stays 0 — never invent occupancy from API totals.
	u := usageUpdate(engine.Usage{
		InputTokens:              100,
		OutputTokens:             20,
		CacheCreationInputTokens: 5,
		CacheReadInputTokens:     15,
		MaxContextTokens:         100_000,
	})
	if u.Used != 0 {
		t.Fatalf("used = %d, want 0 (no EstimatedContextTokens)", u.Used)
	}
	if u.Size != 100_000 {
		t.Fatalf("size = %d, want 100000", u.Size)
	}
}

func TestUsageUpdateFromSessionUsesEstimateNotMetadataTotals(t *testing.T) {
	u := usageUpdateFromSession(42_000, 200_000)
	if u.Used != 42_000 || u.Size != 200_000 {
		t.Fatalf("usage = %#v, want used=42000 size=200000", u)
	}
}

func TestAgentThoughtChunkWireShape(t *testing.T) {
	raw, err := json.Marshal(SessionUpdate{
		SessionUpdate: "agent_thought_chunk",
		Content:       &ContentBlock{Type: "text", Text: "planning next step"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["sessionUpdate"] != "agent_thought_chunk" {
		t.Fatalf("sessionUpdate = %#v", payload["sessionUpdate"])
	}
	content, ok := payload["content"].(map[string]any)
	if !ok {
		t.Fatalf("content = %#v", payload["content"])
	}
	if content["type"] != "text" || content["text"] != "planning next step" {
		t.Fatalf("content = %#v", content)
	}
}

func TestSessionHistoryMapsThinkingToThoughtChunk(t *testing.T) {
	s := &session.Session{
		Messages: []sdk.MessageParam{
			{
				Role: sdk.MessageParamRoleAssistant,
				Content: []sdk.ContentBlockParamUnion{
					{OfThinking: &sdk.ThinkingBlockParam{Thinking: "reason step"}},
					{OfText: &sdk.TextBlockParam{Text: "answer"}},
				},
			},
		},
	}
	updates := sessionHistoryUpdates(s)
	if len(updates) != 2 {
		t.Fatalf("updates = %#v", updates)
	}
	if updates[0].SessionUpdate != "agent_thought_chunk" || updates[0].Content == nil || updates[0].Content.Text != "reason step" {
		t.Fatalf("thought = %#v", updates[0])
	}
	if updates[1].SessionUpdate != "agent_message_chunk" || updates[1].Content == nil || updates[1].Content.Text != "answer" {
		t.Fatalf("message = %#v", updates[1])
	}
}

func TestSessionHistoryHidesDurableDynamicContext(t *testing.T) {
	s := session.NewSession("main", "", "")
	s.Append(sdk.NewUserMessage(sdk.NewTextBlock("task")))
	s.Append(sdk.NewUserMessage(sdk.NewTextBlock(contextmessage.DynamicContextText("mode", "internal policy"))))
	s.Append(sdk.NewAssistantMessage(sdk.NewTextBlock("answer")))
	updates := sessionHistoryUpdates(s)
	if len(updates) != 2 || updates[0].Content.Text != "task" || updates[1].Content.Text != "answer" {
		t.Fatalf("internal overlays leaked into ACP history: %+v", updates)
	}
}
