package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/contextcompression"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func TestAutomaticContextSummaryWithContextTools(t *testing.T) {
	for _, failure := range []string{"", "api", "checkpoint", "nonreducing"} {
		t.Run("failure="+failure, func(t *testing.T) {
			reg := tool.NewRegistry()
			reg.Register(tool.ContextTools(nil, nil)...)
			evidence := strings.Repeat("ARCHIVED_EXACT_EVIDENCE ", 15000) + "END_EXACT_EVIDENCE"
			history := []sdk.MessageParam{
				sdk.NewUserMessage(sdk.NewTextBlock("old request")),
				sdk.NewAssistantMessage(sdk.NewTextBlock(evidence)),
				sdk.NewUserMessage(sdk.NewTextBlock("previous task")),
				sdk.NewAssistantMessage(sdk.NewTextBlock("previous task done")),
			}
			state := contextcompression.New()
			if err := state.Sync(history); err != nil {
				t.Fatal(err)
			}
			client := &midRunClient{}
			summaries, committed := 0, false
			eng := NewEngine(Config{
				Client: client, Tools: reg, ModelName: "chat", MaxTokens: 128, MaxTurns: 1, MaxContextTokens: 30000,
				ContextSummary: func(ctx context.Context, transcript string) (string, error) {
					summaries++
					if !strings.Contains(transcript, evidence) || strings.Contains(transcript, "CURRENT_REQUEST") {
						t.Fatal("summary input lost evidence or included the protected request")
					}
					if failure == "api" {
						return "", errors.New("summary endpoint rejected request")
					}
					if failure == "nonreducing" {
						return transcript + transcript, nil
					}
					return "Previous investigation complete; exact evidence is archived.", nil
				},
			})
			result := eng.RunWithHistory(t.Context(), RunRequest{
				AgentConfig: agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: "CURRENT_REQUEST"},
				Messages:    history, Compression: state,
				Checkpoint: func(cp agent.Checkpoint) error {
					if cp.Stage == "compacted" {
						if failure == "checkpoint" {
							return errors.New("disk unavailable")
						}
						if cp.Compression == nil || cp.Compression.Revision != 1 {
							t.Fatal("fold missing from checkpoint")
						}
						committed = true
					}
					return nil
				},
			})
			if summaries != 1 {
				t.Fatalf("summary calls=%d, want 1", summaries)
			}
			if !strings.Contains(session.Transcript(result.Messages), "CURRENT_REQUEST") {
				t.Fatal("latest request lost")
			}
			if state.Revision != 0 {
				t.Fatal("caller's state mutated")
			}
			if failure != "" {
				if result.AgentResult.Error == "" || !result.AgentResult.NoRetry || client.creates != 0 || committed {
					t.Fatalf("failure was not preserved: %+v creates=%d committed=%v", result.AgentResult, client.creates, committed)
				}
				if result.Compression.Revision != 0 || !strings.Contains(session.Transcript(result.Messages), evidence) {
					t.Fatal("failed compaction changed active history")
				}
				return
			}
			if result.AgentResult.Error != "" || client.creates != 1 || !committed {
				t.Fatalf("result=%+v creates=%d committed=%v", result.AgentResult, client.creates, committed)
			}
			if strings.Contains(session.Transcript(result.Messages), evidence) {
				t.Fatal("raw evidence still active")
			}
			found, err := result.Compression.Search("ARCHIVED_EXACT_EVIDENCE", 0, 10)
			if err != nil || found["total"].(int) < 1 {
				t.Fatal("original evidence lost", err)
			}
		})
	}
}

func TestAutomaticContextSummaryHonorsTrigger(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("old request")),
		sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("completed evidence ", 3000))),
		sdk.NewUserMessage(sdk.NewTextBlock("CURRENT_REQUEST")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("working")),
	}
	builder := ContextBuilder{}
	req := BuildRequest{Messages: messages}
	estimate := builder.EstimateContextTokens(req)
	for _, percent := range []int{80, 95} {
		calls := 0
		eng := NewEngine(Config{MaxContextTokens: estimate * 100 / 90, CompactionTriggerPercent: percent,
			ContextSummary: func(context.Context, string) (string, error) { calls++; return "Completed previous work.", nil },
		})
		_, state, err := eng.compactContext(t.Context(), contextcompression.New(), messages, builder, req)
		if err != nil {
			t.Fatal(err)
		}
		if percent == 80 && (calls != 1 || state == nil) {
			t.Fatal("configured trigger ignored")
		}
		if percent == 95 && (calls != 0 || state != nil) {
			t.Fatal("compaction started below trigger")
		}
	}
}
