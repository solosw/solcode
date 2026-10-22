package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/session"
)

func createConfiguredSession(t *testing.T, client *harnessClient, cwd string) SessionNewResult {
	t.Helper()
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: cwd}), &created); err != nil {
		t.Fatal(err)
	}
	return created
}

func TestACPOnlyAdvertisesDeclaredModelEfforts(t *testing.T) {
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg = configuredACPModels(t, cfg)
		cfg.Providers[1].Models[1].SupportedEfforts = []string{}
		cfg.Providers[1].Models[1].Effort = ""
		return cfg
	}, nil)
	created := createConfiguredSession(t, client, t.TempDir())
	setACPConfig(t, client, created.SessionID, reasoningEffortConfigID, "max")
	options := setACPConfig(t, client, created.SessionID, "model", "gpt/shared")
	if got := findConfigOption(t, options, reasoningEffortConfigID); len(got.Options) != 3 || got.CurrentValue != "max" {
		t.Fatalf("valid effort not preserved: %+v", got)
	}
	options = setACPConfig(t, client, created.SessionID, "model", "gpt/other")
	if len(options) != 2 {
		t.Fatalf("non-reasoning model advertised effort: %+v", options)
	}
	sess := client.server.getSession(created.SessionID)
	if sess.cfg.Effort != "" || sess.cfg.Thinking {
		t.Fatal("non-reasoning model retained reasoning settings")
	}
	options = setACPConfig(t, client, created.SessionID, "model", "claude/shared")
	if got := findConfigOption(t, options, reasoningEffortConfigID).CurrentValue; got != "high" {
		t.Fatalf("invalid target default: %s", got)
	}
	if !sess.cfg.Thinking {
		t.Fatal("switching back did not restore target thinking setting")
	}
}

func TestACPConfigurationFailureDoesNotCommit(t *testing.T) {
	for _, reason := range []string{"credentials", "endpoint", "persistence"} {
		t.Run(reason, func(t *testing.T) {
			client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
				cfg = configuredACPModels(t, cfg)
				if reason == "credentials" {
					cfg.Providers[1].APIKey = ""
				}
				if reason == "endpoint" {
					cfg.Providers[1].BaseURL = "not-a-url"
				}
				return cfg
			}, nil)
			created := createConfiguredSession(t, client, t.TempDir())
			sess := client.server.getSession(created.SessionID)
			if reason == "persistence" {
				// A file in place of the session directory makes the atomic save fail.
				blocker := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				sess.mu.Lock()
				sess.cfg.Session.Dir = blocker
				sess.mu.Unlock()
			}
			before := sess.cfg
			var result SessionSetConfigOptionResult
			err := client.conn.Call(t.Context(), MethodSessionSetConfigOption, SessionSetConfigOptionParams{SessionID: created.SessionID, ConfigID: "model", Value: "gpt/shared"}, &result)
			if rpcErr, ok := err.(*jsonrpcError); !ok || rpcErr.Code != JSONRPCInternalError {
				t.Fatalf("expected runtime failure, got %v", err)
			}
			if !reflect.DeepEqual(sess.cfg, before) || !reflect.DeepEqual(sess.configOptions(), created.ConfigOptions) {
				t.Fatal("failed switch mutated confirmed configuration")
			}
			if sess.application.Config.Provider != "claude" || sess.application.Config.Model != "claude-test" {
				t.Fatal("failed switch replaced runtime")
			}
		})
	}
}

func TestACPSavedConfigurationRestoresAndAdjusts(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			workDir, dir := t.TempDir(), t.TempDir()
			mutate := func(cfg config.Config) config.Config {
				cfg = configuredACPModels(t, cfg)
				cfg.Session.Dir = dir
				return cfg
			}
			first := newACPHarnessWithConfig(t, mutate, nil)
			created := createConfiguredSession(t, first, workDir)
			setACPConfig(t, first, created.SessionID, "model", "gpt/shared")
			setACPConfig(t, first, created.SessionID, reasoningEffortConfigID, "max")
			expected := setACPConfig(t, first, created.SessionID, "mode", "plan")
			path := first.server.getSession(created.SessionID).configStatePath()
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(saved), "test-gpt") || strings.Contains(string(saved), "api_key") {
				t.Fatal("persisted credentials")
			}
			// Keep an existing visible transcript independent of the settings sidecar.
			transcript, err := session.NewFileStore(dir).Load(t.Context(), session.SessionID(created.SessionID))
			if err != nil {
				t.Fatal(err)
			}
			transcript.Messages = []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("retained visible history"))}
			if err := session.NewFileStore(dir).Save(t.Context(), transcript); err != nil {
				t.Fatal(err)
			}
			second := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
				cfg = mutate(cfg)
				if unavailable {
					cfg.Providers = cfg.Providers[:1]
				}
				return cfg
			}, nil)
			mustCall(t, second, MethodInitialize, map[string]any{"protocolVersion": 1})
			var loaded SessionLoadResult
			if err := json.Unmarshal(mustCall(t, second, MethodSessionLoad, SessionLoadParams{SessionID: created.SessionID, Cwd: workDir}), &loaded); err != nil {
				t.Fatal(err)
			}
			if !unavailable && !reflect.DeepEqual(loaded.ConfigOptions, expected) {
				t.Fatalf("saved state not restored: %+v", loaded.ConfigOptions)
			}
			if unavailable {
				if findConfigOption(t, loaded.ConfigOptions, "model").CurrentValue != "claude/shared" {
					t.Fatal("unavailable model was not replaced")
				}
				waitFor(t, func() bool { return hasText(second.snapshot(), "agent_message_chunk", "Saved model is unavailable") })
			}
			waitFor(t, func() bool { return hasText(second.snapshot(), "user_message_chunk", "retained visible history") })
		})
	}
}

func TestACPConfirmedModelChangesNextHTTPCallWithinPrompt(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	oldRequests, newRequests := make(chan string, 2), make(chan string, 2)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		oldRequests <- r.Header.Get("x-api-key") + "\n" + string(data)
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"old","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"visible old answer"},{"type":"thinking","thinking":"opaque old reasoning","signature":"old-provider-signature"},{"type":"tool_use","id":"call-1","name":"missing_test_tool","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer old.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		newRequests <- r.Header.Get("Authorization") + "\n" + string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"new","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"new answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer fresh.Close()
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg = configuredACPModels(t, cfg)
		cfg.Stream = false
		cfg.Providers[0].BaseURL = old.URL
		cfg.BaseURL = old.URL
		cfg.Providers[1].BaseURL = fresh.URL + "/v1"
		return cfg
	}, func(ctx context.Context, a *app.App, _, prompt, dir string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		result := a.Engine.RunWithHistory(ctx, engine.RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, WorkDir: dir, Prompt: prompt, MaxTurns: 3}})
		return result.AgentResult, nil
	})
	created := createConfiguredSession(t, client, t.TempDir())
	done := make(chan error, 1)
	go func() {
		var result SessionPromptResult
		done <- client.conn.Call(t.Context(), MethodSessionPrompt, SessionPromptParams{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: "keep this user message"}}}, &result)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("first model call did not start")
	}
	setACPConfig(t, client, created.SessionID, "model", "gpt/shared")
	setACPConfig(t, client, created.SessionID, reasoningEffortConfigID, "max")
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("prompt did not finish")
	}
	before := <-oldRequests
	if !strings.Contains(before, "test-claude") || !strings.Contains(before, `"model":"claude-test"`) {
		t.Fatalf("old call did not retain its configuration: %s", before)
	}
	select {
	case after := <-newRequests:
		for _, want := range []string{"Bearer test-gpt", `"model":"gpt-test"`, `"reasoning_effort":"max"`, "keep this user message", "visible old answer", "call-1"} {
			if !strings.Contains(after, want) {
				t.Errorf("new request missing %q: %s", want, after)
			}
		}
		if strings.Contains(after, "old-provider-signature") || strings.Contains(after, "opaque old reasoning") || strings.Contains(after, "test-claude") {
			t.Fatal("new provider received old opaque reasoning or credentials")
		}
	default:
		t.Fatal("confirmed model did not affect the next actual call in the same prompt")
	}
}

// Capabilities here are a mock provider contract, not claims about any real
// model. Ensure opaque levels survive ACP -> session -> engine -> HTTP intact.
func TestACPReasoningEffortReachesProviderRequests(t *testing.T) {
	levels := []string{"none", "minimal", "low", "high", "xhigh", "max", "provider-custom"}
	for _, format := range []string{"anthropic", "openai", "responses"} {
		t.Run(format, func(t *testing.T) {
			type capturedRequest struct {
				path string
				body map[string]any
			}
			requests := make(chan capturedRequest, len(levels))
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode provider request: %v", err)
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				requests <- capturedRequest{r.URL.Path, body}
				w.Header().Set("Content-Type", "application/json")
				switch format {
				case "anthropic":
					_, _ = io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"mock-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				case "openai":
					_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
				case "responses":
					_, _ = io.WriteString(w, `{"id":"r1","model":"mock-model","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
				}
			}))
			defer endpoint.Close()
			client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
				cfg.Stream = false
				cfg.Provider, cfg.Model = "mock", "mock-model"
				cfg.Providers = []config.ProviderConfig{{
					Name: "mock", APIKey: "test-only", BaseURL: endpoint.URL, APIFormat: format,
					Models: []config.ModelConfig{{Name: "mock-model", ID: "mock-model", Effort: "high", SupportedEfforts: levels}},
				}}
				if err := cfg.Normalize(); err != nil {
					t.Fatal(err)
				}
				return cfg
			}, func(ctx context.Context, a *app.App, _, prompt, dir string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
				result := a.Engine.RunWithHistory(ctx, engine.RunRequest{AgentConfig: agent.AgentConfig{
					ID: "main", Role: agent.AgentRoleMain, WorkDir: dir, Prompt: prompt, MaxTurns: 1,
				}})
				if result.AgentResult.Error != "" {
					t.Errorf("mock model call failed: %s", result.AgentResult.Error)
				}
				return result.AgentResult, nil
			})
			created := createConfiguredSession(t, client, t.TempDir())
			option := findConfigOption(t, created.ConfigOptions, "reasoning_effort")
			var advertised []string
			for _, choice := range option.Options {
				advertised = append(advertised, choice.Value)
			}
			if !reflect.DeepEqual(advertised, levels) {
				t.Fatalf("hardcoded/reordered effort capabilities: %v", advertised)
			}
			for _, level := range levels {
				setACPConfig(t, client, created.SessionID, "reasoning_effort", level)
				mustCall(t, client, MethodSessionPrompt, SessionPromptParams{
					SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: "verify " + level}},
				})
				select {
				case request := <-requests:
					var effort any
					var wantPath string
					var forbidden []string
					switch format {
					case "anthropic":
						wantPath = "/v1/messages"
						if output, ok := request.body["output_config"].(map[string]any); ok {
							effort = output["effort"]
						}
						forbidden = []string{"reasoning_effort", "reasoning"}
					case "openai":
						wantPath = "/v1/chat/completions"
						effort = request.body["reasoning_effort"]
						forbidden = []string{"output_config", "reasoning"}
					case "responses":
						wantPath = "/v1/responses"
						if reasoning, ok := request.body["reasoning"].(map[string]any); ok {
							effort = reasoning["effort"]
						}
						forbidden = []string{"output_config", "reasoning_effort"}
					}
					if request.path != wantPath || effort != level || request.body["model"] != "mock-model" {
						t.Fatalf("%s: path=%s (want %s), effort=%v, model=%v", level, request.path, wantPath, effort, request.body["model"])
					}
					for _, key := range append(forbidden, "thought_level", "effort") {
						if _, exists := request.body[key]; exists {
							t.Errorf("%s: unexpected transport field %q", level, key)
						}
					}
				default:
					t.Fatalf("%s: confirmed effort did not reach provider", level)
				}
			}
		})
	}
}
