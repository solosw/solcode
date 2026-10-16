package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/memory"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func TestACPNewSessionDefaultsToCrossSessionMemory(t *testing.T) {
	client := newACPHarness(t, func(ctx context.Context, application *app.App, sessionID, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		current, err := application.Sessions.Load(ctx, session.SessionID(sessionID))
		if err != nil {
			return agent.AgentResult{}, err
		}
		if current.Metadata.CrossSessionMemory == nil || !*current.Metadata.CrossSessionMemory || !current.Metadata.MemoryBootstrapPending {
			t.Error("the first prompt must see the persisted opt-in and pending memory bootstrap")
		}
		return agent.AgentResult{Output: "ok"}, nil
	})
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var result SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &result); err != nil {
		t.Fatal(err)
	}
	sess := client.server.getSession(result.SessionID)
	current, err := sess.application.Sessions.Load(t.Context(), session.SessionID(sess.persistID()))
	if err != nil {
		t.Fatal(err)
	}
	if current.Metadata.CrossSessionMemory == nil || !*current.Metadata.CrossSessionMemory || !current.Metadata.MemoryBootstrapPending {
		t.Fatalf("new-session memory metadata = %+v", current.Metadata)
	}
	if len(current.Messages) != 0 {
		t.Fatal("enabling cross-session memory must not copy an older session's transcript")
	}
	if sess.application.Config.Memory.Enabled || sess.application.MemoryManager != nil {
		t.Fatal("a session opt-in must not override the globally disabled memory feature")
	}
	mustCall(t, client, MethodSessionPrompt, map[string]any{
		"sessionId": result.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": "continue"}},
	})
}

func TestACPLoadSessionPreservesCrossSessionMemoryPreference(t *testing.T) {
	for _, choice := range []string{"unset", "disabled", "enabled"} {
		t.Run(choice, func(t *testing.T) {
			workDir, sessionDir := t.TempDir(), t.TempDir()
			store := session.NewFileStore(sessionDir)
			current := session.NewSession("existing", workDir, "test-model")
			if choice != "unset" {
				enabled := choice == "enabled"
				current.Metadata.CrossSessionMemory = &enabled
			}
			if err := store.Save(t.Context(), current); err != nil {
				t.Fatal(err)
			}
			client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
				cfg.WorkDir, cfg.Session.Dir = workDir, sessionDir
				return cfg
			}, nil)
			mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
			mustCall(t, client, MethodSessionLoad, map[string]any{"sessionId": "existing", "cwd": workDir})
			loaded, err := store.Load(t.Context(), "existing")
			if err != nil {
				t.Fatal(err)
			}
			got := loaded.Metadata.CrossSessionMemory
			if (choice == "unset" && got != nil) || (choice != "unset" && (got == nil || *got != (choice == "enabled"))) {
				t.Fatalf("loading changed the %s memory preference: %+v", choice, loaded.Metadata)
			}
			if loaded.Metadata.MemoryBootstrapPending {
				t.Fatal("loading must not re-arm an already consumed memory bootstrap")
			}
		})
	}
}

func TestInitializeACPNewSessionMemoryRespectsExplicitChoicesAndOwnedLock(t *testing.T) {
	for _, choice := range []string{"fresh", "unset", "disabled", "enabled"} {
		t.Run(choice, func(t *testing.T) {
			store := session.NewFileStore(t.TempDir())
			manager := session.NewManager(store, "current")
			t.Cleanup(func() { _ = manager.ReleaseAll() })
			current, _, err := manager.LoadOrCreateAndAcquire(t.Context(), "current", t.TempDir(), "test-model")
			if err != nil {
				t.Fatal(err)
			}
			if choice != "unset" && choice != "fresh" {
				enabled := choice == "enabled"
				current.Metadata.CrossSessionMemory = &enabled
			}
			if choice != "fresh" {
				if err := manager.Save(t.Context(), current); err != nil {
					t.Fatal(err)
				}
			}
			sess := &acpSession{id: "wire-id", diskID: "current", application: &app.App{Sessions: manager}}
			if err := sess.initializeNewSessionMemory(t.Context()); err != nil {
				t.Fatalf("initialization must reuse the factory-owned session lock: %v", err)
			}
			loaded, err := manager.Load(t.Context(), "current")
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Metadata.CrossSessionMemory == nil || *loaded.Metadata.CrossSessionMemory != (choice != "disabled") {
				t.Fatalf("initialization lost the %s preference: %+v", choice, loaded.Metadata)
			}
			if loaded.Metadata.MemoryBootstrapPending != (choice == "unset" || choice == "fresh") {
				t.Fatalf("initialization unexpectedly changed bootstrap state for %s", choice)
			}
		})
	}
}

func TestACPNewSessionWithoutPersistenceStillWorks(t *testing.T) {
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.Session.Enabled, cfg.Session.Persist = false, false
		return cfg
	}, nil)
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var result SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID == "" || client.server.getSession(result.SessionID).application.Sessions != nil {
		t.Fatal("creating a session must respect disabled persistence")
	}
}

func TestACPNewSessionMemoryFailureDoesNotLeaveSessionRegistered(t *testing.T) {
	invalidDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidDir, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.Session.Dir = invalidDir
		return cfg
	}, nil)
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var result json.RawMessage
	err := client.conn.Call(t.Context(), MethodSessionNew, map[string]any{"cwd": t.TempDir()}, &result)
	if rpcErr, ok := err.(*jsonrpcError); !ok || rpcErr.Code != JSONRPCInternalError || !strings.Contains(rpcErr.Message, "session memory") {
		t.Fatalf("session/new failure = %v, want session-memory initialization error", err)
	}
	client.server.mu.Lock()
	remaining := len(client.server.sessions)
	client.server.mu.Unlock()
	if remaining != 0 {
		t.Fatal("failed memory initialization left an ACP session registered")
	}
}

func TestACPNewSessionCanReadEarlierSessionsMemoryByDefault(t *testing.T) {
	client := newACPHarness(t, nil)
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var result SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &result); err != nil {
		t.Fatal(err)
	}
	sess := client.server.getSession(result.SessionID)
	application := sess.application
	// Use a local-only memory manager so this exercises the real read gate
	// without a provider request or native model initialization.
	application.Config.Memory.Enabled = true
	application.MemoryStore = memory.NewFileStore(t.TempDir())
	application.MemoryManager = memory.NewManager(application.MemoryStore, memory.DefaultGate{}, memory.StaticJudge{})
	written, err := application.WriteMemory(t.Context(), tool.MemoryWriteRequest{
		Text:      "Run the repository's CLI tests before committing changes.",
		Kind:      "workflow",
		Scope:     "project",
		SessionID: "earlier-session",
	})
	if err != nil || !written.Stored {
		t.Fatalf("seed memory = %+v, error = %v", written, err)
	}
	read, err := application.ReadMemory(t.Context(), tool.MemoryReadRequest{SessionID: sess.persistID(), Limit: 10})
	if err != nil || !read.CrossSessionAllowed {
		t.Fatalf("new ACP session did not allow cross-session reads: %+v, error = %v", read, err)
	}
	found := false
	for _, entry := range read.Entries {
		found = found || (entry.OtherSession && entry.Text == written.Text)
	}
	if !found {
		t.Fatalf("new ACP session could not retrieve earlier-session memory: %+v", read)
	}
}
