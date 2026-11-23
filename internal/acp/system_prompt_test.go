package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
)

func requireRPCError(t *testing.T, err error, code int) {
	t.Helper()
	var rpcErr *jsonrpcError
	if !errors.As(err, &rpcErr) || rpcErr.Code != code {
		t.Fatalf("RPC error = %v, want code %d", err, code)
	}
}

func TestACPSystemPromptCapability(t *testing.T) {
	client := newACPHarness(t, nil)
	var result map[string]any
	if err := json.Unmarshal(mustCall(t, client, MethodInitialize, InitializeParams{ProtocolVersion: 1}), &result); err != nil {
		t.Fatal(err)
	}
	caps := result["agentCapabilities"].(map[string]any)["sessionCapabilities"].(map[string]any)
	prompt := caps["systemPrompt"].(map[string]any)
	if _, ok := prompt["mode"].(map[string]any)["override"].(map[string]any); !ok {
		t.Fatalf("missing override object: %v", prompt)
	}
}

func TestACPRejectsInvalidSystemPromptBeforeChangingSession(t *testing.T) {
	client := newACPHarness(t, nil)
	created := createConfiguredSession(t, client, t.TempDir())
	live := client.server.getSession(created.SessionID)
	for _, raw := range []string{
		`{}`,
		`{"mode":"unknown","content":[{"type":"text","text":"x"}]}`,
		`{"mode":"append","content":[]}`,
		`{"mode":"override","content":null}`,
		`{"mode":"append","content":[{"type":"text","text":" "}]}`,
		`{"mode":"append","content":[{"type":"image","data":"x"}]}`,
		`{"mode":"override","content":[{"type":"text","text":"x"},{"type":"resource_link","uri":"file:///x"}]}`,
	} {
		for _, method := range []string{MethodSessionNew, MethodSessionLoad} {
			params := map[string]any{"cwd": live.workDir, "sessionId": created.SessionID, "systemPrompt": json.RawMessage(raw)}
			var result json.RawMessage
			err := client.conn.Call(t.Context(), method, params, &result)
			requireRPCError(t, err, JSONRPCInvalidParams)
			if client.server.getSession(created.SessionID) != live {
				t.Fatal("invalid lifecycle request replaced live session")
			}
			client.server.mu.Lock()
			count := len(client.server.sessions)
			client.server.mu.Unlock()
			if count != 1 {
				t.Fatalf("invalid request created a session: %d", count)
			}
		}
	}
}

func TestACPSystemPromptReachesModelAndLoad(t *testing.T) {
	for _, mode := range []string{"append", "override"} {
		t.Run(mode, func(t *testing.T) {
			requests := make(chan map[string]any, 6)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", 400)
					return
				}
				requests <- request
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"mock-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer endpoint.Close()
			workDir, sessionDir := t.TempDir(), t.TempDir()
			mutate := func(cfg config.Config) config.Config {
				cfg.WorkDir, cfg.Session.Dir, cfg.SystemPrompt = workDir, sessionDir, "Configured core preamble."
				cfg.Stream = false
				cfg.Provider, cfg.Model = "mock", "mock-model"
				cfg.Providers = []config.ProviderConfig{{Name: "mock", APIKey: "test-only", BaseURL: endpoint.URL, APIFormat: "anthropic", Models: []config.ModelConfig{{Name: "mock-model", ID: "mock-model"}}}}
				if err := cfg.Normalize(); err != nil {
					t.Fatal(err)
				}
				return cfg
			}
			run := func(ctx context.Context, a *app.App, _, prompt, dir string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
				result := a.Engine.RunWithHistory(ctx, engine.RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, WorkDir: dir, Prompt: prompt, MaxTurns: 1}})
				if result.AgentResult.Error != "" {
					t.Errorf("model call: %s", result.AgentResult.Error)
				}
				return result.AgentResult, nil
			}
			client := newACPHarnessWithConfig(t, mutate, run)
			mustCall(t, client, MethodInitialize, InitializeParams{ProtocolVersion: 1})
			var created SessionNewResult
			instructions := &SystemPrompt{Mode: mode, Content: []ContentBlock{{Type: "text", Text: "Client core A."}, {Type: "text", Text: "Client core B."}}}
			if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: workDir, SystemPrompt: instructions}), &created); err != nil {
				t.Fatal(err)
			}
			send := func(c *harnessClient, id string) map[string]any {
				mustCall(t, c, MethodSessionPrompt, SessionPromptParams{SessionID: id, Prompt: []ContentBlock{{Type: "text", Text: "ordinary user request"}}})
				select {
				case req := <-requests:
					return req
				default:
					t.Fatal("model was not called")
					return nil
				}
			}
			check := func(req map[string]any, marker string, defaults bool) {
				system, _ := json.Marshal(req["system"])
				for _, want := range []string{marker, "Tool usage:", "Skills:", "PLAN MODE", "Working directory:"} {
					if !strings.Contains(string(system), want) {
						t.Errorf("model system missing %q", want)
					}
				}
				if strings.Contains(string(system), "You are solcode") != defaults || strings.Contains(string(system), "Configured core preamble.") != defaults {
					t.Error("core override/append was not applied to actual model system")
				}
				if tools, ok := req["tools"].([]any); !ok || len(tools) == 0 {
					t.Error("core edit removed tool schemas")
				}
				messages, _ := json.Marshal(req["messages"])
				if strings.Contains(string(messages), marker) {
					t.Error("system instructions leaked into user messages")
				}
			}
			check(send(client, created.SessionID), "Client core A.", mode == "append")
			// Rebuilding the engine through a normal configuration change retains the core.
			setACPConfig(t, client, created.SessionID, "mode", "bypass")
			check(send(client, created.SessionID), "Client core B.", mode == "append")
			var other SessionNewResult
			if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: workDir}), &other); err != nil {
				t.Fatal(err)
			}
			plain := send(client, other.SessionID)
			system, _ := json.Marshal(plain["system"])
			if strings.Contains(string(system), "Client core") {
				t.Fatal("client instructions leaked into another session")
			}
			client.server.Close()
			restored := newACPHarnessWithConfig(t, mutate, run)
			mustCall(t, restored, MethodInitialize, InitializeParams{ProtocolVersion: 1})
			instructions.Mode, instructions.Content = "override", []ContentBlock{{Type: "text", Text: "Replacement on load."}}
			mustCall(t, restored, MethodSessionLoad, SessionLoadParams{SessionID: created.SessionID, Cwd: workDir, SystemPrompt: instructions})
			check(send(restored, created.SessionID), "Replacement on load.", false)
			// Prompts are request-scoped and not restored from saved configuration.
			restored.server.Close()
			defaults := newACPHarnessWithConfig(t, mutate, run)
			mustCall(t, defaults, MethodInitialize, InitializeParams{ProtocolVersion: 1})
			mustCall(t, defaults, MethodSessionLoad, SessionLoadParams{SessionID: created.SessionID, Cwd: workDir})
			check(send(defaults, created.SessionID), "Configured core preamble.", true)
		})
	}
}
