package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/contextcompression"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func contextWindowHistory() []sdk.MessageParam {
	var history []sdk.MessageParam
	for i := 0; i < 18; i++ {
		history = append(history, sdk.NewUserMessage(sdk.NewTextBlock(fmt.Sprintf("old request %d", i))), sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat(fmt.Sprintf("OLD_EVIDENCE_%d ", i), 500))))
	}
	return history
}

func TestContextWindowOverflowContinuesAndPersists(t *testing.T) {
	for _, compactOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(compactOnly), func(t *testing.T) {
			history := contextWindowHistory()
			if compactOnly {
				history = append(history, sdk.NewUserMessage(sdk.NewTextBlock("LATEST_REQUEST")), sdk.NewAssistantMessage(sdk.NewTextBlock("recent answer")))
			}
			registry := tool.NewRegistry()
			registry.Register(tool.ContextTools(nil, nil)...)
			state := contextcompression.New()
			if err := state.Sync(history); err != nil {
				t.Fatal(err)
			}
			var window agent.Checkpoint
			calls := 0
			client := compressionRequestClientFunc(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				calls++
				if window.Stage != "context_window" {
					t.Fatal("request sent before window checkpoint")
				}
				if req.ForceTool != "" {
					t.Fatal("overflow must truncate directly without summary")
				}
				text := session.Transcript(req.Messages)
				if strings.Contains(text, "OLD_EVIDENCE_0 ") || !strings.Contains(text, "LATEST_REQUEST") || !strings.Contains(text, "OLD_EVIDENCE_17 ") {
					t.Fatal("wrong recent window")
				}
				return &sdk.Message{Role: "assistant", StopReason: sdk.StopReasonEndTurn, Content: []sdk.ContentBlockUnion{{Type: "text", Text: "continued"}}}, nil
			})
			eng := NewEngine(Config{Client: client, Tools: registry, MaxContextTokens: 40000, MaxTokens: 2000, MaxTurns: 1})
			result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: "LATEST_REQUEST", MaxTurns: 1}, Messages: history, Compression: state, CompactOnly: compactOnly, SkipPrompt: compactOnly, Checkpoint: func(cp agent.Checkpoint) error {
				if cp.Stage == "context_window" {
					window = cp
				}
				return nil
			}})
			if result.AgentResult.Error != "" {
				t.Fatal(result.AgentResult.Error)
			}
			if compactOnly && calls != 0 || !compactOnly && (calls != 1 || result.AgentResult.Output != "continued") {
				t.Fatalf("calls=%d output=%q", calls, result.AgentResult.Output)
			}
			if result.Compression.Revision != 1 || state.Revision != 0 {
				t.Fatal("wrong window revision or caller mutation")
			}
			if !window.Compacted || window.Compression.Revision != 1 {
				t.Fatal("window not checkpointed")
			}
			// Simulate an actual persisted session reload, including source references.
			saved := session.NewSession("main", t.TempDir(), "chat")
			saved.ReplaceMessages(window.Messages)
			saved.Compression = window.Compression
			store := session.NewManager(session.NewFileStore(t.TempDir()), "main")
			if err := store.Save(t.Context(), saved); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.LoadOrCreate(t.Context(), "main", saved.Metadata.WorkDir, "chat")
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Messages) != len(window.Messages) {
				t.Fatal("reload restored discarded window")
			}
			active, err := loaded.Compression.Messages()
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(active)
			b, _ := json.Marshal(loaded.Messages)
			if string(a) != string(b) {
				t.Fatal("active refs disagree with saved messages")
			}
			found, err := loaded.Compression.Search("OLD_EVIDENCE_0", 0, 10)
			if err != nil || found["total"].(int) == 0 {
				t.Fatal("archive lost", err)
			}
		})
	}
}

func TestContextWindowUpstreamRejectionRetriesWithoutUsingTurn(t *testing.T) {
	for _, failure := range []string{"context_length_exceeded", "unrelated", "checkpoint"} {
		t.Run(failure, func(t *testing.T) {
			history := contextWindowHistory()
			registry := tool.NewRegistry()
			registry.Register(tool.ContextTools(nil, nil)...)
			calls, windows := 0, 0
			firstSize := 0
			client := compressionRequestClientFunc(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				calls++
				if calls == 1 {
					firstSize = len(req.Messages)
					if failure == "unrelated" {
						return nil, errors.New("HTTP 401 invalid API key")
					}
					return nil, errors.New("HTTP 400 context_length_exceeded")
				}
				if len(req.Messages) >= firstSize || windows != 1 {
					t.Fatal("rejected history retried unchanged")
				}
				return &sdk.Message{Role: "assistant", StopReason: sdk.StopReasonEndTurn, Content: []sdk.ContentBlockUnion{{Type: "text", Text: "recovered"}}}, nil
			})
			eng := NewEngine(Config{Client: client, Tools: registry, MaxContextTokens: 500000, MaxTokens: 2000, MaxTurns: 1})
			result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: "LATEST_REQUEST", MaxTurns: 1}, Messages: history, Checkpoint: func(cp agent.Checkpoint) error {
				if cp.Stage == "context_window" {
					windows++
					if failure == "checkpoint" {
						return errors.New("disk unavailable")
					}
				}
				return nil
			}})
			if failure == "context_length_exceeded" {
				if calls != 2 || result.AgentResult.Error != "" || result.AgentResult.Output != "recovered" {
					t.Fatalf("calls=%d result=%+v", calls, result.AgentResult)
				}
			} else {
				if calls != 1 || result.AgentResult.Error == "" || !strings.Contains(session.Transcript(result.Messages), "OLD_EVIDENCE_0 ") {
					t.Fatal("failed recovery discarded history or retried")
				}
				if result.Compression.Revision != 0 {
					t.Fatal("failed save advanced window")
				}
			}
		})
	}
}

func TestRecentContextWindowKeepsCurrentRequestAndParallelToolGroups(t *testing.T) {
	messages := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("LATEST_REQUEST"))}
	for i := 0; i < 12; i++ {
		a, b := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		messages = append(messages, sdk.NewAssistantMessage(sdk.NewToolUseBlock(a, json.RawMessage(`{}`), "View"), sdk.NewToolUseBlock(b, json.RawMessage(`{}`), "View")), sdk.NewUserMessage(sdk.NewToolResultBlock(a, strings.Repeat("large earlier evidence ", 1000), false)), sdk.NewUserMessage(sdk.NewToolResultBlock(b, "done", false)))
	}
	builder := ContextBuilder{}
	req := BuildRequest{Messages: messages, MaxTokens: 2000}
	next, err := recentContextWindow(messages, builder, req, 40000, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages[0], next[0]) || len(next) >= len(messages) {
		t.Fatal("lost current request or failed to shrink")
	}
	pending := map[string]bool{}
	for _, m := range next {
		for _, b := range m.Content {
			if b.OfToolUse != nil {
				pending[b.OfToolUse.ID] = true
			}
			if b.OfToolResult != nil {
				if !pending[b.OfToolResult.ToolUseID] {
					t.Fatal("orphaned parallel result")
				}
				delete(pending, b.OfToolResult.ToolUseID)
			}
		}
	}
	if len(pending) > 0 {
		t.Fatal("split tool exchange")
	}
	req.Messages = next
	if builder.EstimateContextTokens(req) > 28000 {
		t.Fatal("window above target")
	}
}

func TestContextWindowRepeatedRejectionIsBounded(t *testing.T) {
	calls, windows := 0, 0
	eng := NewEngine(Config{Client: compressionRequestClientFunc(func(_ context.Context, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		return nil, errors.New("HTTP 400 maximum context length exceeded")
	}), MaxContextTokens: 500000, MaxTokens: 2000, MaxTurns: 1})
	result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Prompt: "LATEST_REQUEST", WorkDir: t.TempDir(), MaxTurns: 1}, Messages: contextWindowHistory(), Checkpoint: func(cp agent.Checkpoint) error {
		if cp.Stage == "context_window" {
			windows++
		}
		return nil
	}})
	if calls != 4 || windows != 3 || result.AgentResult.Error == "" || !result.AgentResult.NoRetry {
		t.Fatalf("calls=%d windows=%d result=%+v", calls, windows, result.AgentResult)
	}
}

func TestRecentContextWindowCannotFitLatestRequest(t *testing.T) {
	messages := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("old")), sdk.NewAssistantMessage(sdk.NewTextBlock("done")), sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("HUGE_LATEST ", 10000)))}
	before, _ := json.Marshal(messages)
	if _, err := recentContextWindow(messages, ContextBuilder{}, BuildRequest{Messages: messages, MaxTokens: 2000}, 10000, false); err == nil {
		t.Fatal("oversized latest request unexpectedly fits")
	}
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("failed trim modified messages")
	}
}
