package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestAutonomousCompressionCheckpointsAndCachePrefix(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "failed commit"}[failSave], func(t *testing.T) {
			reg := tool.NewRegistry()
			reg.Register(tool.ContextTools(nil, nil)...)
			raw := strings.Repeat("ARCHIVED_EXACT_EVIDENCE ", 400)
			history := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("stable prefix")), sdk.NewAssistantMessage(sdk.NewTextBlock(raw)), sdk.NewUserMessage(sdk.NewTextBlock("old requirement")), sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("completed work ", 200)))}
			var saved agent.Snapshot
			client := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				switch index {
				case 0:
					return backgroundMessage(t, backgroundCall("status", "acp_status", map[string]any{})), nil
				case 1:
					texts := backgroundResultTexts(req.Messages, "status")
					if len(texts) != 1 || !strings.Contains(texts[0], "m2") {
						t.Fatalf("missing context refs: %v", texts)
					}
					return backgroundMessage(t, backgroundCall("fold", "compress", map[string]any{"start": "m2", "end": "m4", "summary": "Completed investigation. Keep the current request.", "revision": 0})), nil
				case 2:
					texts := backgroundResultTexts(req.Messages, "fold")
					if len(texts) != 1 {
						t.Fatal(texts)
					}
					wire, _ := json.Marshal(req.Messages)
					if failSave {
						if !strings.Contains(texts[0], "disk failed") || !strings.Contains(string(wire), "ARCHIVED_EXACT_EVIDENCE") {
							t.Fatal("failed commit discarded original")
						}
					} else {
						if !strings.Contains(texts[0], "b1") || strings.Contains(string(wire), "ARCHIVED_EXACT_EVIDENCE") {
							t.Fatal("fold not applied")
						}
					}
					return backgroundMessage(t), nil
				default:
					t.Fatalf("unexpected additional model call %d", index)
					return nil, nil
				}
			}}
			eng := NewEngine(Config{Client: client, Tools: reg, ModelName: "chat", SystemPrompt: "stable system", Permissions: permission.NewService(permission.ModeBypass), MaxTurns: 4})
			result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, WorkDir: t.TempDir(), Prompt: "CURRENT request"}, Messages: history, Checkpoint: func(cp agent.Checkpoint) error {
				if failSave && cp.Stage == "context_compress" {
					return errors.New("disk failed")
				}
				saved = cp.Snapshot
				return nil
			}})
			if result.AgentResult.Error != "" {
				t.Fatal(result.AgentResult.Error)
			}
			if len(client.requests) != 3 {
				t.Fatalf("extra summarizer request: %d", len(client.requests))
			}
			first, last := client.requests[0], client.requests[2]
			a, _ := json.Marshal(first.Messages[0])
			b, _ := json.Marshal(last.Messages[0])
			if string(a) != string(b) || first.System != last.System {
				t.Fatal("public prefix changed")
			}
			a, _ = json.Marshal(first.Tools)
			b, _ = json.Marshal(last.Tools)
			if string(a) != string(b) {
				t.Fatal("tool schemas changed")
			}
			if !failSave {
				if saved.Compression == nil || result.Compression == nil {
					t.Fatal("compression state not checkpointed")
				}
				found, err := saved.Compression.Search("ARCHIVED_EXACT_EVIDENCE", 0, 10)
				if err != nil || found["total"].(int) < 1 {
					t.Fatal("archive lost", err)
				}
			}
		})
	}
}
func TestAutonomousContextBudgetNeverSilentlyTruncates(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.ContextTools(nil, nil)...)
	client := &midRunClient{}
	oldCalls := 0
	eng := NewEngine(Config{Client: client, Tools: reg, MaxContextTokens: 10, CompactMessages: func(_ context.Context, m []sdk.MessageParam) ([]sdk.MessageParam, error) { oldCalls++; return nil, nil }})
	result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: strings.Repeat("huge request ", 100)}})
	if result.AgentResult.Error == "" || client.creates != 0 || oldCalls != 0 || len(result.Messages) == 0 {
		t.Fatalf("budget handling: error=%s requests=%d old_compact=%d", result.AgentResult.Error, client.creates, oldCalls)
	}
}
