package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

func TestACPEmptySessionLoadsAfterRestart(t *testing.T) {
	for _, configOnly := range []bool{false, true} {
		name := "persisted"
		if configOnly {
			name = "config-only"
		}
		t.Run(name, func(t *testing.T) {
			workDir, dir := t.TempDir(), t.TempDir()
			mutate := func(cfg config.Config) config.Config {
				cfg.WorkDir, cfg.Session.Dir = workDir, dir
				return cfg
			}
			first := newACPHarnessWithConfig(t, mutate, nil)
			mustCall(t, first, MethodInitialize, map[string]any{"protocolVersion": 1})
			var created SessionNewResult
			if err := json.Unmarshal(mustCall(t, first, MethodSessionNew, SessionNewParams{Cwd: workDir}), &created); err != nil {
				t.Fatal(err)
			}
			expected := setACPConfig(t, first, created.SessionID, "mode", "plan")
			first.server.Close()
			store := session.NewFileStore(dir)
			if configOnly {
				// Older ACP sessions may have only saved configuration before the first prompt.
				if err := store.Delete(t.Context(), session.SessionID(created.SessionID)); err != nil {
					t.Fatal(err)
				}
			}
			second := newACPHarnessWithConfig(t, mutate, func(ctx context.Context, application *app.App, id, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
				current, err := application.Sessions.Load(ctx, session.SessionID(id))
				if err != nil {
					return agent.AgentResult{}, err
				}
				if len(current.Messages) != 0 || !current.Metadata.MemoryBootstrapPending {
					t.Error("first prompt lost empty-session bootstrap")
				}
				return agent.AgentResult{Output: "ok"}, nil
			})
			mustCall(t, second, MethodInitialize, map[string]any{"protocolVersion": 1})
			var loaded SessionLoadResult
			if err := json.Unmarshal(mustCall(t, second, MethodSessionLoad, SessionLoadParams{SessionID: created.SessionID, Cwd: workDir}), &loaded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(loaded.ConfigOptions, expected) {
				t.Fatalf("config after reload = %+v, want %+v", loaded.ConfigOptions, expected)
			}
			current, err := store.Load(t.Context(), session.SessionID(created.SessionID))
			if err != nil {
				t.Fatal(err)
			}
			if len(current.Messages) != 0 || current.Metadata.WorkDir != workDir || current.Metadata.CrossSessionMemory == nil || !*current.Metadata.CrossSessionMemory || !current.Metadata.MemoryBootstrapPending {
				t.Fatalf("empty-session state = %+v", current)
			}
			for _, update := range second.snapshot() {
				if update.Update.SessionUpdate == "user_message_chunk" || update.Update.SessionUpdate == "agent_message_chunk" {
					t.Fatalf("empty session replayed a message: %+v", update)
				}
			}
			mustCall(t, second, MethodSessionPrompt, SessionPromptParams{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: "hello"}}})
		})
	}
}

func TestACPEmptySessionLoadRejectsMissingOrCorruptState(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt-transcript", "corrupt-config"} {
		t.Run(kind, func(t *testing.T) {
			dir, workDir := t.TempDir(), t.TempDir()
			mutate := func(cfg config.Config) config.Config { cfg.WorkDir, cfg.Session.Dir = workDir, dir; return cfg }
			id := "missing"
			if kind != "missing" {
				first := newACPHarnessWithConfig(t, mutate, nil)
				mustCall(t, first, MethodInitialize, map[string]any{"protocolVersion": 1})
				var created SessionNewResult
				if err := json.Unmarshal(mustCall(t, first, MethodSessionNew, SessionNewParams{Cwd: workDir}), &created); err != nil {
					t.Fatal(err)
				}
				id = created.SessionID
				configPath := first.server.getSession(id).configStatePath()
				first.server.Close()
				if kind == "corrupt-config" {
					if err := session.NewFileStore(dir).Delete(t.Context(), session.SessionID(id)); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(configPath, []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			second := newACPHarnessWithConfig(t, mutate, nil)
			mustCall(t, second, MethodInitialize, map[string]any{"protocolVersion": 1})
			var loaded SessionLoadResult
			err := second.conn.Call(t.Context(), MethodSessionLoad, SessionLoadParams{SessionID: id, Cwd: workDir}, &loaded)
			code := JSONRPCInternalError
			if kind == "missing" {
				code = JSONRPCInvalidParams
			}
			if rpcErr, ok := err.(*jsonrpcError); !ok || rpcErr.Code != code {
				t.Fatalf("load error = %v, want code %d", err, code)
			}
			if second.server.getSession(id) != nil {
				t.Fatal("failed load left a registered session")
			}
			if kind != "corrupt-transcript" {
				if _, err := session.NewFileStore(dir).Load(t.Context(), session.SessionID(id)); !session.IsNotFound(err) {
					t.Fatalf("failed load created transcript: %v", err)
				}
			}
		})
	}
}
