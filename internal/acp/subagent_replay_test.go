package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

type replayRunner struct{}

func (replayRunner) Run(_ context.Context, cfg agent.AgentConfig) agent.AgentResult {
	return agent.AgentResult{Output: "reply " + cfg.Prompt}
}

func TestACPSubagentReplayAfterRestart(t *testing.T) {
	workDir, sessionDir := t.TempDir(), t.TempDir()
	main := session.NewSession("loaded", workDir, "test-model")
	main.Append(sdk.NewUserMessage(sdk.NewTextBlock("INHERITED_MAIN_ONLY")))
	if err := session.NewFileStore(sessionDir).Save(t.Context(), main); err != nil {
		t.Fatal(err)
	}
	store := agentsession.NewFileStore(sessionDir)
	writer := agent.NewCoordinator(replayRunner{}, store)
	spawn := func(cfg agent.AgentConfig) {
		t.Helper()
		if _, err := writer.Spawn(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		if result, err := writer.Wait(t.Context(), cfg.ID); err != nil || result.Error != "" {
			t.Fatalf("spawn result=%+v %v", result, err)
		}
	}
	childConfig := agent.AgentConfig{ID: "child", RunID: "child-run", ParentSessionID: "loaded", ParentID: "main", WorkDir: workDir, Description: "Explore", Prompt: "CHILD_REQUEST", ForkContext: "full", ParentContext: agent.Snapshot{Messages: main.Messages}}
	spawn(childConfig)
	spawn(agent.AgentConfig{ID: "nested", RunID: "nested-run", ParentSessionID: "child", ParentID: "child", WorkDir: workDir, Description: "Nested", Prompt: "NESTED_REQUEST"})
	// A reusable child may later serve a different project session. Loading the
	// original conversation must recover only the original runs and their output.
	childConfig.ParentSessionID, childConfig.RunID, childConfig.Prompt = "other-session", "foreign-run", "FOREIGN_RUN_REQUEST"
	if _, err := writer.Resume(t.Context(), childConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Wait(t.Context(), childConfig.ID); err != nil {
		t.Fatal(err)
	}
	spawn(agent.AgentConfig{ID: "foreign-nested", RunID: "foreign-nested-run", ParentSessionID: "child", ParentID: "child", WorkDir: workDir, Description: "Foreign nested", Prompt: "FOREIGN_NESTED_REQUEST"})
	spawn(agent.AgentConfig{ID: "foreign", RunID: "foreign-root-run", ParentSessionID: "other-session", ParentID: "main", WorkDir: workDir, Description: "Unrelated", Prompt: "FOREIGN_CHILD_REQUEST"})
	spawn(agent.AgentConfig{ID: "other-project", RunID: "other-project-run", ParentSessionID: "loaded", ParentID: "main", WorkDir: t.TempDir(), Description: "Other project", Prompt: "FOREIGN_PROJECT_REQUEST"})
	now := time.Now()
	interrupted := &agent.Record{Config: agent.AgentConfig{ID: "interrupted", ParentSessionID: "loaded", ParentID: "main", WorkDir: workDir, Description: "Interrupted"}, Status: agent.AgentStatus{ID: "interrupted", RunID: "interrupted-run", State: agent.AgentRunning}, Runs: []agent.RunRecord{{ID: "interrupted-run", ParentSessionID: "loaded", ParentID: "main", Description: "Interrupted", State: agent.AgentRunning}}, Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("SAVED_IN_PROGRESS_REQUEST"))}, Events: []agent.SessionEvent{{RunID: "interrupted-run", MessageCount: 1}}, CreatedAt: now, UpdatedAt: now}
	if err := store.Save(t.Context(), interrupted); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for attempt := 0; attempt < 2; attempt++ {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
				cfg.WorkDir = workDir
				cfg.Session.Enabled, cfg.Session.Persist = true, true
				cfg.Session.Dir = sessionDir
				cfg.Memory.Enabled, cfg.KnowledgeGraph.Enabled = false, false
				disabled := false
				cfg.CodeGraph.Enabled = &disabled
				return cfg
			}, func(_ context.Context, _ *app.App, _, _, _ string, _ int, emit StreamEmitter) (agent.AgentResult, error) {
				emit.AgentProgress(tool.AgentProgressEvent{Kind: "started", AgentID: "child", RunID: "resumed-run", Description: "Explore"})
				emit.AgentProgress(tool.AgentProgressEvent{Kind: "text_delta", AgentID: "child", RunID: "resumed-run", Output: "resumed stream"})
				emit.AgentProgress(tool.AgentProgressEvent{Kind: "completed", AgentID: "child", RunID: "resumed-run"})
				return agent.AgentResult{}, nil
			})
			mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"subagents": map[string]any{}}})
			mustCall(t, client, MethodSessionLoad, map[string]any{"sessionId": "loaded", "cwd": workDir})
			updates := client.snapshot()
			announced := map[string]bool{"loaded": true}
			childText := 0
			interruptedState := false
			for _, p := range updates {
				u := p.Update
				if u.SessionUpdate == "subagent_update" && u.Capabilities != nil {
					if !announced[p.SessionID] {
						t.Fatalf("child announced before parent: %+v", p)
					}
					announced[u.SessionID] = true
					if previous := ids[u.Title]; previous != "" && previous != u.SessionID {
						t.Fatalf("child identity changed after restart: %s -> %s", previous, u.SessionID)
					}
					ids[u.Title] = u.SessionID
					if u.Title == "Nested" && p.SessionID != ids["Explore"] {
						t.Fatalf("nested child attached to %s", p.SessionID)
					}
				}
				if p.SessionID != "loaded" && !announced[p.SessionID] {
					t.Fatalf("child content before discovery: %+v", p)
				}
				wire, _ := json.Marshal(p)
				if strings.Contains(string(wire), "FOREIGN_") || strings.Contains(string(wire), "Other project") || strings.Contains(string(wire), "Unrelated") {
					t.Fatalf("unrelated child history leaked: %s", wire)
				}
				if u.Content != nil && p.SessionID != "loaded" {
					if strings.Contains(u.Content.Text, "INHERITED_MAIN_ONLY") {
						t.Fatal("parent fork replayed as child output")
					}
					if u.Content.Text == "reply CHILD_REQUEST" {
						childText++
					}
				}
				if u.State != nil && u.SessionID == ids["Interrupted"] {
					interruptedState = u.State.State == "idle" && u.State.StopReason == "error" && u.State.Error != nil && strings.Contains(u.State.Error.Message, "interrupted")
				}
			}
			if len(announced) != 4 || childText != 1 || !interruptedState || !hasText(updates, "agent_message_chunk", "reply NESTED_REQUEST") {
				t.Fatalf("incomplete restored children: ids=%+v text=%d interrupted=%v", ids, childText, interruptedState)
			}
			before := len(updates)
			mustCall(t, client, MethodSessionPrompt, map[string]any{"sessionId": "loaded", "prompt": []map[string]string{{"type": "text", "text": "resume"}}})
			for _, p := range client.snapshot()[before:] {
				if p.Update.Capabilities != nil {
					t.Fatal("resumed child announced as a new conversation")
				}
				if p.Update.Content != nil && p.Update.Content.Text == "resumed stream" && p.SessionID != ids["Explore"] {
					t.Fatal("resumed stream lost restored identity")
				}
			}
		})
	}
}

type unavailableReplayStore struct{ agent.Store }

func (unavailableReplayStore) List(context.Context) ([]*agent.Record, error) {
	return nil, errors.New("child store unreadable")
}

func TestACPSubagentReplayCapabilityAndReadErrors(t *testing.T) {
	s, root, output := newSubagentRecorder()
	root.application = &app.App{Coordinator: agent.NewCoordinator(nil, unavailableReplayStore{})}
	root.subagentsEnabled = false
	if err := s.replayPersistedSubagents(t.Context(), root); err != nil || output.Len() != 0 {
		t.Fatal("legacy client attempted subagent replay", err)
	}
	root.subagentsEnabled = true
	if err := s.replayPersistedSubagents(t.Context(), root); err == nil || !strings.Contains(err.Error(), "child store unreadable") {
		t.Fatal("storage failure was hidden", err)
	}
}

func TestSubagentReplayMessagesRespectRunOwnership(t *testing.T) {
	record := &agent.Record{Status: agent.AgentStatus{RunID: "ours-2"}, Messages: []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("ours-1")), sdk.NewAssistantMessage(sdk.NewTextBlock("foreign")), sdk.NewUserMessage(sdk.NewTextBlock("ours-2")),
	}, Events: []agent.SessionEvent{{RunID: "ours-1", MessageCount: 1}, {RunID: "foreign", MessageCount: 2}, {RunID: "ours-2", MessageCount: 3}}}
	out := subagentReplayMessages(record, map[string]bool{"ours-1": true, "ours-2": true}, false)
	if len(out) != 2 || len(out["ours-1"]) != 1 || len(out["ours-2"]) != 1 {
		t.Fatalf("history=%+v", out)
	}
	wire, _ := json.Marshal(out)
	if strings.Contains(string(wire), "foreign") {
		t.Fatal("foreign run leaked")
	}
	record.Events = nil
	if len(subagentReplayMessages(record, map[string]bool{"ours-2": true}, false)) != 0 {
		t.Fatal("ambiguous legacy history leaked")
	}
}
