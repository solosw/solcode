package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/session"
)

func findConfigOption(t *testing.T, options []SessionConfigOption, id string) SessionConfigOption {
	t.Helper()
	for _, option := range options {
		if option.ID == id {
			return option
		}
	}
	t.Fatalf("missing configuration option %q: %+v", id, options)
	return SessionConfigOption{}
}

func assertConfigOptions(t *testing.T, options []SessionConfigOption) {
	t.Helper()
	if len(options) < 2 || len(options) > 3 {
		t.Fatalf("expected model/mode and optional thinking configuration: %+v", options)
	}
	for _, option := range options {
		id := option.ID
		wantCategory := id
		if id == reasoningEffortConfigID {
			wantCategory = "thought_level"
		}
		if option.Category != wantCategory || option.Name == "" || option.Type != "select" || !configOptionHasValue(option, option.CurrentValue) {
			t.Fatalf("invalid %s option: %+v", id, option)
		}
		seen := make(map[string]bool)
		for _, value := range option.Options {
			if seen[value.Value] || value.Value == "" || value.Name == "" {
				t.Fatalf("invalid or duplicate option value: %+v", value)
			}
			seen[value.Value] = true
		}
	}
}

func configuredACPModels(t *testing.T, cfg config.Config) config.Config {
	t.Helper()
	cfg.Providers = []config.ProviderConfig{
		{Name: "claude", APIKey: "test-claude", APIFormat: "anthropic", BaseURL: "https://claude.example", Models: []config.ModelConfig{
			{Name: "shared", ID: "claude-test", DisplayName: "Claude test", Effort: "high", SupportedEfforts: []string{"low", "medium", "high", "xhigh", "max"}, MaxContextTokens: 120000},
		}},
		{Name: "gpt", APIKey: "test-gpt", APIFormat: "openai", BaseURL: "https://gpt.example/v1", Models: []config.ModelConfig{
			{Name: "shared", ID: "gpt-test", DisplayName: "GPT test", Effort: "low", SupportedEfforts: []string{"low", "medium", "max"}, MaxContextTokens: 64000},
			{Name: "other", ID: "gpt-other", Effort: "medium", SupportedEfforts: []string{"medium", "high", "max"}},
		}},
	}
	cfg.Provider, cfg.Model = "claude", "shared"
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestACPConfigOptionsNewAndLoad(t *testing.T) {
	workDir, sessionDir := t.TempDir(), t.TempDir()
	if err := session.NewFileStore(sessionDir).Save(t.Context(), session.NewSession("config-loaded", workDir, "test-model")); err != nil {
		t.Fatal(err)
	}
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.WorkDir, cfg.Session.Dir = workDir, sessionDir
		return configuredACPModels(t, cfg)
	}, nil)
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: workDir}), &created); err != nil {
		t.Fatal(err)
	}
	assertConfigOptions(t, created.ConfigOptions)
	if created.Modes == nil || findConfigOption(t, created.ConfigOptions, "mode").CurrentValue != created.Modes.CurrentModeID {
		t.Fatal("legacy modes and configuration options are out of sync")
	}
	var loaded SessionLoadResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionLoad, SessionLoadParams{SessionID: "config-loaded", Cwd: workDir}), &loaded); err != nil {
		t.Fatal(err)
	}
	assertConfigOptions(t, loaded.ConfigOptions)
	if !reflect.DeepEqual(created.ConfigOptions, loaded.ConfigOptions) {
		t.Fatal("new and load advertised different configuration options")
	}
}

func setACPConfig(t *testing.T, client *harnessClient, sessionID, id, value string) []SessionConfigOption {
	t.Helper()
	var result SessionSetConfigOptionResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionSetConfigOption, SessionSetConfigOptionParams{SessionID: sessionID, ConfigID: id, Value: value}), &result); err != nil {
		t.Fatal(err)
	}
	assertConfigOptions(t, result.ConfigOptions)
	if got := findConfigOption(t, result.ConfigOptions, id).CurrentValue; got != value {
		t.Fatalf("%s currentValue = %q, want %q", id, got, value)
	}
	return result.ConfigOptions
}

func TestACPSetConfigOptionAppliesModelModeAndEffort(t *testing.T) {
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		return configuredACPModels(t, cfg)
	}, func(_ context.Context, application *app.App, _, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		if application.Config.Model != "gpt-test" || application.Config.Provider != "gpt" || application.Config.Effort != "max" || application.Permissions.Mode() != permission.ModePlan || application.Client.Format() != "openai" || application.Config.MaxContextTokens != 64000 {
			t.Errorf("configuration did not reach application/client: model=%s provider=%s effort=%s mode=%s format=%s context=%d", application.Config.Model, application.Config.Provider, application.Config.Effort, application.Permissions.Mode(), application.Client.Format(), application.Config.MaxContextTokens)
		}
		return agent.AgentResult{Output: "ok"}, nil
	})
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	model := findConfigOption(t, created.ConfigOptions, "model")
	if len(model.Options) != 3 || model.CurrentValue != "claude/shared" {
		t.Fatalf("all-provider catalog missing or ambiguous: %+v", model)
	}
	options := setACPConfig(t, client, created.SessionID, "model", "gpt/shared")
	if got := findConfigOption(t, options, reasoningEffortConfigID).CurrentValue; got != "low" {
		t.Fatalf("model-dependent effort was not returned: %q", got)
	}
	setACPConfig(t, client, created.SessionID, "mode", "plan")
	options = setACPConfig(t, client, created.SessionID, reasoningEffortConfigID, "max")
	if reselected := setACPConfig(t, client, created.SessionID, "model", "gpt/shared"); !reflect.DeepEqual(options, reselected) {
		t.Fatal("reselecting the current model reset explicit thinking effort")
	}
	waitFor(t, func() bool {
		for _, update := range client.snapshot() {
			if update.SessionID == created.SessionID && update.Update.SessionUpdate == "config_option_update" && reflect.DeepEqual(update.Update.ConfigOptions, options) {
				return true
			}
		}
		return false
	})
	mustCall(t, client, MethodSessionPrompt, SessionPromptParams{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: "verify"}}})
	// Option changes are scoped to one ACP session, not the server defaults.
	var other SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: t.TempDir()}), &other); err != nil {
		t.Fatal(err)
	}
	if findConfigOption(t, other.ConfigOptions, "model").CurrentValue != "claude/shared" || findConfigOption(t, other.ConfigOptions, reasoningEffortConfigID).CurrentValue != "high" {
		t.Fatal("configuration changes leaked into a new session")
	}
}

func TestACPSetConfigOptionRejectsInvalidRequests(t *testing.T) {
	client := newACPHarness(t, nil)
	var result json.RawMessage
	err := client.conn.Call(t.Context(), MethodSessionSetConfigOption, SessionSetConfigOptionParams{SessionID: "missing", ConfigID: "mode", Value: "plan"}, &result)
	if rpcErr, ok := err.(*jsonrpcError); !ok || rpcErr.Code != JSONRPCInvalidRequest {
		t.Fatalf("uninitialized request error = %v", err)
	}
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	for _, params := range []map[string]any{
		{"sessionId": "missing", "configId": "mode", "value": "plan"},
		{"sessionId": "", "configId": "mode", "value": "plan"},
		{"configId": "missing", "value": "plan"},
		{"configId": "mode", "value": "unknown"},
		{"configId": "mode", "value": "ultrl"}, // New select values must be advertised canonical IDs.
		{"configId": "model", "value": "not-advertised"},
		{"configId": reasoningEffortConfigID, "value": "invalid"},
		{"configId": reasoningEffortConfigID, "value": true},
		{"configId": "mode", "type": "boolean", "value": "plan"},
		{"configId": "mode", "type": "id", "value": "plan"},
		{"configId": "mode"},
	} {
		if _, ok := params["sessionId"]; !ok {
			params["sessionId"] = created.SessionID
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		err := client.conn.Call(ctx, MethodSessionSetConfigOption, params, &result)
		cancel()
		if rpcErr, ok := err.(*jsonrpcError); !ok || rpcErr.Code != JSONRPCInvalidParams {
			t.Fatalf("params=%v: error=%v, want invalid params", params, err)
		}
	}
	sess := client.server.getSession(created.SessionID)
	if got := sess.configOptions(); !reflect.DeepEqual(got, created.ConfigOptions) {
		t.Fatal("invalid request modified session configuration")
	}
}

func TestACPConfigOptionsFollowLegacyAndAgentModeChanges(t *testing.T) {
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.PermissionMode, cfg.Permissions.Mode = permission.ModePlan, permission.ModePlan
		return cfg
	}, func(ctx context.Context, application *app.App, _, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		if err := application.SwitchMode(ctx, "ultrl"); err != nil {
			return agent.AgentResult{}, err
		}
		return agent.AgentResult{Output: "ok"}, nil
	})
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	mustCall(t, client, MethodSessionSetMode, SessionSetModeParams{SessionID: created.SessionID, ModeID: "ultrl"})
	waitForConfigMode(t, client, "ultra")
	mustCall(t, client, MethodSessionSetMode, SessionSetModeParams{SessionID: created.SessionID, ModeID: "plan"})
	mustCall(t, client, MethodSessionPrompt, SessionPromptParams{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: "switch mode"}}})
	waitForConfigMode(t, client, "ultra")
	if got := findConfigOption(t, client.server.getSession(created.SessionID).configOptions(), "mode").CurrentValue; got != "ultra" {
		t.Fatalf("agent mode change did not update config options: %s", got)
	}
}

func waitForConfigMode(t *testing.T, client *harnessClient, mode string) {
	t.Helper()
	waitFor(t, func() bool {
		updates := client.snapshot()
		for i := len(updates) - 1; i >= 0; i-- {
			if updates[i].Update.SessionUpdate == "config_option_update" {
				return findConfigOption(t, updates[i].Update.ConfigOptions, "mode").CurrentValue == mode
			}
		}
		return false
	})
}

func TestACPConfigOptionsCanChangeDuringPrompt(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		return configuredACPModels(t, cfg)
	}, func(ctx context.Context, application *app.App, _, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		calls++
		if calls == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return agent.AgentResult{}, ctx.Err()
			}
			if application.Config.Model != "claude-test" || application.Config.Effort != "high" {
				t.Error("active prompt's model/effort were replaced mid-run")
			}
			if application.Permissions.Mode() != permission.ModePlan {
				t.Error("permission mode change did not take effect immediately")
			}
		} else if application.Config.Model != "gpt-test" || application.Config.Effort != "max" || application.Config.PermissionMode != permission.ModePlan {
			t.Errorf("next prompt did not receive staged settings: %+v", application.Config)
		}
		return agent.AgentResult{Output: "ok"}, nil
	})
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	params := SessionPromptParams{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: "work"}}}
	done := make(chan error, 1)
	go func() {
		var result SessionPromptResult
		done <- client.conn.Call(t.Context(), MethodSessionPrompt, params, &result)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not start")
	}
	setACPConfig(t, client, created.SessionID, "model", "gpt/shared")
	setACPConfig(t, client, created.SessionID, reasoningEffortConfigID, "max")
	setACPConfig(t, client, created.SessionID, "mode", "plan")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mustCall(t, client, MethodSessionPrompt, params)
}

func TestConfigOptionUpdateJSONRoundTrip(t *testing.T) {
	original := SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: sessionConfigOptions(configuredACPModels(t, config.Default()))}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"configOptions"`) || !strings.Contains(string(raw), `"category":"thought_level"`) || !strings.Contains(string(raw), `"currentValue"`) {
		t.Fatalf("invalid wire format: %s", raw)
	}
	var decoded SessionUpdate
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip differs: %+v", decoded)
	}
	if err := json.Unmarshal([]byte(`{"sessionUpdate":"config_option_update","configOptions":"invalid"}`), &decoded); err == nil {
		t.Fatal("invalid configOptions accepted")
	}
}

func TestConfigModelValuesAreUnambiguous(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.ProviderConfig{
		{Name: "a/b", Models: []config.ModelConfig{{Name: "c", ID: "one"}}},
		{Name: "a", Models: []config.ModelConfig{{Name: "b/c", ID: "two"}}},
	}
	cfg.Provider, cfg.Model = "a", "two"
	choices, current := configModelChoices(cfg)
	if len(choices) != 2 || choices[0].Value == choices[1].Value || current != choices[1].Value {
		t.Fatalf("ambiguous qualified models: %+v; current=%s", choices, current)
	}
	assertConfigOptions(t, sessionConfigOptions(cfg))
	assertConfigOptions(t, sessionConfigOptions(config.Default()))
}

func TestACPConfigOptionsFollowSlashSelections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		return configuredACPModels(t, cfg)
	}, nil)
	requests := make(chan jsonrpcMessage, 1)
	client.mu.Lock()
	client.permissionRequests = requests
	client.mu.Unlock()
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command, choice, id, want string
	}{
		{"/provider", "gpt", "model", "gpt/shared"},
		{"/model", "other", "model", "gpt/other"},
		{"/effort", "max", reasoningEffortConfigID, "max"},
	} {
		done := make(chan error, 1)
		go func() {
			var result SessionPromptResult
			done <- client.conn.Call(t.Context(), MethodSessionPrompt, SessionPromptParams{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: tc.command}}}, &result)
		}()
		var request jsonrpcMessage
		select {
		case request = <-requests:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not request a selection", tc.command)
		}
		if err := client.conn.Reply(request.ID, RequestPermissionResult{Outcome: PermissionOutcome{Outcome: "selected", OptionID: tc.choice}}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			updates := client.snapshot()
			for i := len(updates) - 1; i >= 0; i-- {
				if updates[i].Update.SessionUpdate == "config_option_update" {
					return findConfigOption(t, updates[i].Update.ConfigOptions, tc.id).CurrentValue == tc.want
				}
			}
			return false
		})
	}
}

func TestACPReasoningEffortIDAndLegacyAlias(t *testing.T) {
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		return configuredACPModels(t, cfg)
	}, nil)
	created := createConfiguredSession(t, client, t.TempDir())
	thought := findConfigOption(t, created.ConfigOptions, "reasoning_effort")
	if thought.Category != "thought_level" {
		t.Fatalf("reasoning category = %q", thought.Category)
	}

	// Old clients may still send the previously published ID. The response and
	// notification must contain only the canonical ID, not a second control.
	var result SessionSetConfigOptionResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionSetConfigOption, SessionSetConfigOptionParams{
		SessionID: created.SessionID, ConfigID: "thought_level", Value: "low",
	}), &result); err != nil {
		t.Fatal(err)
	}
	assertConfigOptions(t, result.ConfigOptions)
	if got := findConfigOption(t, result.ConfigOptions, "reasoning_effort").CurrentValue; got != "low" {
		t.Fatalf("legacy alias did not update reasoning effort: %q", got)
	}
	for _, option := range result.ConfigOptions {
		if option.ID == "thought_level" {
			t.Fatal("published a duplicate legacy reasoning selector")
		}
	}
	waitFor(t, func() bool {
		for _, update := range client.snapshot() {
			if update.Update.SessionUpdate == "config_option_update" && reflect.DeepEqual(update.Update.ConfigOptions, result.ConfigOptions) {
				return true
			}
		}
		return false
	})
	canonical := setACPConfig(t, client, created.SessionID, "reasoning_effort", "max")
	var repeated SessionSetConfigOptionResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionSetConfigOption, SessionSetConfigOptionParams{
		SessionID: created.SessionID, ConfigID: "thought_level", Value: "max",
	}), &repeated); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(canonical, repeated.ConfigOptions) {
		t.Fatal("same-value legacy alias changed the confirmed snapshot")
	}

	for _, id := range []string{"reasoning_effort", "thought_level"} {
		var ignored SessionSetConfigOptionResult
		err := client.conn.Call(t.Context(), MethodSessionSetConfigOption, SessionSetConfigOptionParams{
			SessionID: created.SessionID, ConfigID: id, Value: "unsupported",
		}, &ignored)
		if rpcErr, ok := err.(*jsonrpcError); !ok || rpcErr.Code != JSONRPCInvalidParams {
			t.Fatalf("%s accepted an unsupported effort: %v", id, err)
		}
		if !reflect.DeepEqual(canonical, client.server.getSession(created.SessionID).configOptions()) {
			t.Fatal("rejected effort modified confirmed state")
		}
	}
}

func TestACPReasoningIDsCannotEnableDisabledCapabilities(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%v", explicit), func(t *testing.T) {
			client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
				if explicit {
					cfg.SupportedEfforts = []string{}
				} else {
					cfg.Thinking = false
				}
				return cfg
			}, nil)
			created := createConfiguredSession(t, client, t.TempDir())
			if len(created.ConfigOptions) != 2 {
				t.Fatalf("disabled reasoning selector: %+v", created.ConfigOptions)
			}
			for _, id := range []string{"reasoning_effort", "thought_level"} {
				var ignored SessionSetConfigOptionResult
				err := client.conn.Call(t.Context(), MethodSessionSetConfigOption, SessionSetConfigOptionParams{
					SessionID: created.SessionID, ConfigID: id, Value: "high",
				}, &ignored)
				if rpcErr, ok := err.(*jsonrpcError); !ok || rpcErr.Code != JSONRPCInvalidParams {
					t.Fatalf("%s bypassed absent capabilities: %v", id, err)
				}
			}
		})
	}
}
