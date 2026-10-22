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
	setACPConfig(t, client, created.SessionID, "thought_level", "max")
	options := setACPConfig(t, client, created.SessionID, "model", "gpt/shared")
	if got := findConfigOption(t, options, "thought_level"); len(got.Options) != 3 || got.CurrentValue != "max" {
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
	if got := findConfigOption(t, options, "thought_level").CurrentValue; got != "high" {
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
			setACPConfig(t, first, created.SessionID, "thought_level", "max")
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
	setACPConfig(t, client, created.SessionID, "thought_level", "max")
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
