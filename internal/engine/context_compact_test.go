package engine

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/contextcompression"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func TestMandatoryCompressContinuesOriginalTask(t *testing.T) {
	for _, failure := range []string{"", "api", "checkpoint", "nonreducing", "ignored", "other tool", "stale once", "nil message", "truncated"} {
		t.Run(failure, func(t *testing.T) {
			reg := tool.NewRegistry()
			reg.Register(tool.ContextTools(nil, nil)...)
			evidence := strings.Repeat("ARCHIVED_EXACT_EVIDENCE ", 5000) + "END_EXACT_EVIDENCE"
			history := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("stable prefix")), sdk.NewAssistantMessage(sdk.NewTextBlock(evidence)), sdk.NewUserMessage(sdk.NewTextBlock("previous task")), sdk.NewAssistantMessage(sdk.NewTextBlock("previous task done"))}
			committed, resumed, forced := false, false, 0
			client := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				if req.ForceTool == "" {
					resumed = true
					if !committed || !req.Stream || !req.Thinking || req.Effort != "high" {
						t.Fatal("task resumed before commit or lost original model settings")
					}
					if !strings.Contains(session.Transcript(req.Messages), "CURRENT_REQUEST") || strings.Contains(session.Transcript(req.Messages), evidence) {
						t.Fatal("continuation lost task or kept folded history")
					}
					return &sdk.Message{Role: "assistant", StopReason: sdk.StopReasonEndTurn, Content: []sdk.ContentBlockUnion{{Type: "text", Text: "Original task completed"}}}, nil
				}
				forced++
				if req.ForceTool != "compress" || !req.Stream || req.Thinking || req.Effort != "" {
					t.Fatal("compression must force compress and preserve streaming without thinking")
				}
				if failure == "nil message" {
					return nil, nil
				}
				if failure == "truncated" {
					return &sdk.Message{Role: "assistant", StopReason: sdk.StopReasonMaxTokens}, nil
				}
				if failure == "api" {
					return nil, errors.New("endpoint unavailable")
				}
				if failure == "ignored" {
					return backgroundMessage(t), nil
				}
				if failure == "other tool" {
					return backgroundMessage(t, backgroundCall("wrong", "acp_status", map[string]any{})), nil
				}
				summary := "Previous investigation complete; exact evidence is archived. Continue the active task."
				start := "m2"
				if failure == "nonreducing" {
					start, summary = "m3", strings.Repeat("cannot reduce ", 100)
				}
				revision := 0
				if failure == "stale once" && forced == 1 {
					revision = 99
				}
				return backgroundMessage(t, backgroundCall("fold", "compress", map[string]any{"start": start, "end": "m4", "summary": summary, "revision": revision})), nil
			}}
			eng := NewEngine(Config{Client: client, Tools: reg, ModelName: "chat", SystemPrompt: "stable system", MaxTokens: 1024, MaxTurns: 1, MaxContextTokens: 120000, CompactionTriggerPercent: 10, Stream: true, Thinking: true, Effort: "high", Permissions: permission.NewService(permission.ModeBypass)})
			state := contextcompression.New()
			_ = state.Sync(history)
			result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", WorkDir: t.TempDir(), Prompt: "CURRENT_REQUEST", MaxTurns: 1}, Messages: history, Compression: state, Checkpoint: func(cp agent.Checkpoint) error {
				if cp.Stage == "context_compress" {
					if failure == "checkpoint" {
						return errors.New("disk unavailable")
					}
					if cp.Compression.Revision != 1 {
						t.Fatal("missing archive checkpoint")
					}
					committed = true
				}
				return nil
			}})
			if state.Revision != 0 {
				t.Fatal("caller state changed")
			}
			if failure != "" && failure != "stale once" {
				if result.AgentResult.Error == "" || !result.AgentResult.NoRetry || committed || resumed {
					t.Fatalf("failure not preserved: %+v", result.AgentResult)
				}
				if !strings.Contains(session.Transcript(result.Messages), evidence) || result.Compression.Revision != 0 {
					t.Fatalf("failure lost history: revision=%d evidence=%v error=%s", result.Compression.Revision, strings.Contains(session.Transcript(result.Messages), evidence), result.AgentResult.Error)
				}
				want := 1
				if failure == "checkpoint" || failure == "nonreducing" {
					want = 3
				}
				if forced != want {
					t.Fatalf("forced calls=%d want %d", forced, want)
				}
				return
			}
			if result.AgentResult.Error != "" || result.AgentResult.Output != "Original task completed" || !resumed || !committed {
				t.Fatalf("no task continuation: %+v forced=%d", result.AgentResult, forced)
			}
			if failure == "stale once" && forced != 2 {
				t.Fatal("invalid compress was not retried")
			}
			found, err := result.Compression.Search("ARCHIVED_EXACT_EVIDENCE", 0, 10)
			if err != nil || found["total"].(int) == 0 {
				t.Fatal("original evidence lost", err)
			}
			first, last := client.requests[0], client.requests[len(client.requests)-1]
			a, _ := json.Marshal(first.Tools)
			b, _ := json.Marshal(last.Tools)
			if string(a) != string(b) || first.System != last.System {
				t.Fatal("stable tool/system prefix changed")
			}
		})
	}
}

func TestMandatoryCompressHonorsTriggerAndExplicitRequest(t *testing.T) {
	messages := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("old request")), sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("completed evidence ", 3000))), sdk.NewUserMessage(sdk.NewTextBlock("CURRENT_REQUEST")), sdk.NewAssistantMessage(sdk.NewTextBlock("working"))}
	builder := ContextBuilder{}
	req := BuildRequest{Messages: messages}
	estimate := builder.EstimateContextTokens(req)
	for _, tc := range []struct {
		percent        int
		explicit, want bool
	}{{80, false, true}, {95, false, false}, {95, true, true}} {
		eng := NewEngine(Config{MaxContextTokens: estimate * 100 / 90, CompactionTriggerPercent: tc.percent})
		state := contextcompression.New()
		state.Nudge = contextcompression.NudgeState{Initialized: true, LastLevel: "compressed", BaselineTokens: int(estimate)}
		_, forced, err := eng.prepareForcedCompression(state, messages, builder, req, tc.explicit)
		if err != nil || forced != tc.want {
			t.Fatalf("case=%+v forced=%v error=%v", tc, forced, err)
		}
	}
}
