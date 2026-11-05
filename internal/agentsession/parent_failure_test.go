package agentsession_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
)

type parentFailingStore struct {
	agent.Store
	fail atomic.Bool
}

func (s *parentFailingStore) Save(ctx context.Context, r *agent.Record) error {
	if s.fail.Load() && len(r.ParentRequests) > 0 {
		return errors.New("parent mailbox save failed")
	}
	return s.Store.Save(ctx, r)
}

type parentFailureRunner func(context.Context, agent.SessionRequest) agent.AgentResult

func (f parentFailureRunner) Run(context.Context, agent.AgentConfig) agent.AgentResult {
	panic("persistent runner required")
}
func (f parentFailureRunner) RunSession(ctx context.Context, r agent.SessionRequest) agent.AgentResult {
	return f(ctx, r)
}

func TestParentRequestSaveFailureDoesNotPublishOrConsume(t *testing.T) {
	for _, stage := range []string{"question", "answer"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			store := &parentFailingStore{Store: agentsession.NewFileStore(t.TempDir())}
			store.fail.Store(stage == "question")
			var c *agent.Coordinator
			c = agent.NewCoordinator(parentFailureRunner(func(ctx context.Context, req agent.SessionRequest) agent.AgentResult {
				messages := []sdk.MessageParam{sdk.NewAssistantMessage(sdk.NewToolUseBlock("ask", map[string]any{"question": "choose"}, "AskParent"))}
				if err := req.Save(agent.Checkpoint{Snapshot: agent.Snapshot{Messages: messages}, Stage: "response"}); err != nil {
					return agent.AgentResult{Error: err.Error()}
				}
				answer, err := c.AskParent(ctx, req.Config.ID, req.Config.RunID, "ask", "choose", "")
				if err != nil {
					return agent.AgentResult{Error: err.Error()}
				}
				messages = append(messages, sdk.NewUserMessage(sdk.NewToolResultBlock("ask", answer, false)))
				if err := req.Save(agent.Checkpoint{Snapshot: agent.Snapshot{Messages: messages}, Stage: "tool_result"}); err != nil {
					return agent.AgentResult{Error: err.Error()}
				}
				return agent.AgentResult{Output: answer}
			}), store)
			defer c.Close()
			needsInput := make(chan struct{}, 1)
			c.SetEventHandler(func(e agent.Event) {
				if e.Kind == agent.EventNeedsInput {
					needsInput <- struct{}{}
				}
			})
			cfg := config()
			if _, err := c.Spawn(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if stage == "question" {
				result, err := c.Wait(ctx, cfg.ID)
				if err != nil || !strings.Contains(result.Error, "mailbox save failed") {
					t.Fatalf("failed save result=%+v err=%v", result, err)
				}
				select {
				case <-needsInput:
					t.Fatal("unsaved question was published")
				default:
				}
				saved, err := store.Load(ctx, cfg.ID)
				if err != nil || len(saved.ParentRequests) != 0 {
					t.Fatalf("unsaved question persisted: %+v %v", saved, err)
				}
				return
			}
			select {
			case <-needsInput:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			questions, err := c.ParentQuestions(ctx, scope(cfg))
			if err != nil || len(questions) != 1 {
				t.Fatalf("questions=%+v err=%v", questions, err)
			}
			q := questions[0]
			store.fail.Store(true)
			if err := c.ReplyParent(ctx, scope(cfg), q.AgentID, q.RunID, q.ID, "answer"); err == nil {
				t.Fatal("failed answer save succeeded")
			}
			saved, err := store.Load(ctx, cfg.ID)
			if err != nil || saved.ParentRequests[0].AnsweredAt != nil {
				t.Fatalf("failed answer consumed: %+v %v", saved, err)
			}
			_, live, pending := c.ParentActivity(scope(cfg))
			if !live || !pending {
				t.Fatal("failed answer woke or lost the waiting request")
			}
			store.fail.Store(false)
			if err := c.ReplyParent(ctx, scope(cfg), q.AgentID, q.RunID, q.ID, "answer"); err != nil {
				t.Fatal(err)
			}
			result, err := c.Wait(ctx, cfg.ID)
			if err != nil || result.Error != "" || result.Output != "answer" {
				t.Fatalf("retry result=%+v err=%v", result, err)
			}
		})
	}
}
