package engine

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/contextcompression"
	contextmessage "github.com/solosw/solcode/internal/message"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func budgetSnapshot(t *testing.T, messages []sdk.MessageParam) contextcompression.Overview {
	t.Helper()
	for i := len(messages) - 1; i >= 0; i-- {
		for _, b := range messages[i].Content {
			if b.OfText == nil {
				continue
			}
			kind, text, ok := contextmessage.ParseDynamicContextText(b.OfText.Text)
			if !ok || kind != "context-budget" {
				continue
			}
			var o contextcompression.Overview
			lines := strings.Split(text, "\n")
			if len(lines) < 2 {
				t.Fatal(text)
			}
			if err := json.Unmarshal([]byte(lines[1]), &o); err != nil {
				t.Fatal(err)
			}
			return o
		}
	}
	t.Fatal("missing context-budget snapshot")
	return contextcompression.Overview{}
}

func TestReminderCompressesWithoutStatusRoundTrip(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.ContextTools(nil, nil)...)
	history := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("stable prefix")),
		sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("completed evidence ", 20_000))),
		sdk.NewUserMessage(sdk.NewTextBlock("old requirement")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("old work complete")),
	}
	base := ContextBuilder{SystemPrompt: "stable system"}.EstimateContextTokens(BuildRequest{Messages: history, Tools: reg.All(), WorkDir: t.TempDir()})
	var saved agent.Snapshot
	client := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		switch index {
		case 0:
			o := budgetSnapshot(t, req.Messages)
			if len(o.CompressibleRanges) == 0 || len(o.ProtectedRanges) == 0 {
				t.Fatalf("%+v", o)
			}
			r := o.CompressibleRanges[0]
			// Preserve the prefix by selecting a subset with valid endpoints.
			return backgroundMessage(t, backgroundCall("fold", "compress", map[string]any{"start": "m2", "end": r.End, "summary": "Completed investigation; preserve the current user request.", "revision": o.Revision})), nil
		case 1:
			results := backgroundResultTexts(req.Messages, "fold")
			if len(results) != 1 || !strings.Contains(results[0], "b1") {
				t.Fatalf("direct compress failed: %v", results)
			}
			for _, m := range req.Messages {
				for _, b := range m.Content {
					if b.OfText != nil && strings.Contains(b.OfText.Text, "completed evidence completed evidence") {
						t.Fatal("raw evidence still on wire")
					}
				}
			}
			return backgroundMessage(t), nil
		default:
			t.Fatalf("unexpected call %d", index)
			return nil, nil
		}
	}}
	eng := NewEngine(Config{Client: client, Tools: reg, ModelName: "chat", SystemPrompt: "stable system", Permissions: permission.NewService(permission.ModeBypass), MaxTurns: 3, MaxContextTokens: base * 5 / 4})
	result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: "CURRENT request"}, Messages: history, Checkpoint: func(cp agent.Checkpoint) error { saved = cp.Snapshot; return nil }})
	if result.AgentResult.Error != "" {
		t.Fatal(result.AgentResult.Error)
	}
	if len(client.requests) != 2 || saved.Compression == nil || saved.Compression.Revision != 1 {
		t.Fatalf("requests=%d saved=%+v", len(client.requests), saved.Compression)
	}
	if contextcompression.Text(client.requests[0].Messages[0]) != contextcompression.Text(client.requests[1].Messages[0]) {
		t.Fatal("prefix changed")
	}
}

func TestReminderCountsSystemAndToolsAndCannotOverflowRequest(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("old task")),
		sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("evidence ", 1000))),
		sdk.NewUserMessage(sdk.NewTextBlock("current task")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("working")),
	}
	builder := ContextBuilder{SystemPrompt: strings.Repeat("policy ", 20_000)}
	req := BuildRequest{Messages: messages}
	total := builder.EstimateContextTokens(req)
	budget := total * 5 / 4
	state := contextcompression.New()
	e := NewEngine(Config{MaxContextTokens: budget, ContextNudge: contextcompression.NudgeConfig{MinBenefitTokens: 1}})
	out, err := e.appendContextNudge(state, messages, builder, req)
	if err != nil {
		t.Fatal(err)
	}
	if int64(contextcompression.Tokens(messages)) >= budget*3/4 {
		t.Fatal("fixture relies on message-only pressure")
	}
	if len(out) != len(messages)+1 {
		t.Fatal("fixed prompt pressure ignored")
	}
	budgetSnapshot(t, out)
	// A reminder must never turn a request that fits into one that overflows.
	state = contextcompression.New()
	e.config.MaxContextTokens = total + 1
	out, err = e.appendContextNudge(state, messages, builder, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages = out
	if builder.EstimateContextTokens(req) >= e.config.MaxContextTokens {
		t.Fatal("reminder overflowed request")
	}
	if state.Nudge.LastLevel != "" {
		t.Fatal("unsent reminder consumed cooldown")
	}
}
