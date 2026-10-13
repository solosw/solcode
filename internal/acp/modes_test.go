package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/session"
)

func assertCanonicalUltraState(t *testing.T, state *SessionModeState) {
	t.Helper()
	if state == nil || state.CurrentModeID != "ultra" {
		t.Fatalf("current mode state = %+v, want ultra", state)
	}
	seen := make(map[string]bool)
	for _, mode := range state.AvailableModes {
		if seen[mode.ID] || mode.ID == "ultrl" {
			t.Fatalf("unexpected advertised mode %+v", mode)
		}
		seen[mode.ID] = true
	}
	if !seen["ultra"] || len(seen) != len(permission.AvailableModes()) {
		t.Fatalf("available modes = %v", seen)
	}
}

func TestACPNewAndLoadAdvertiseCanonicalUltra(t *testing.T) {
	workDir, sessionDir := t.TempDir(), t.TempDir()
	store := session.NewFileStore(sessionDir)
	if err := store.Save(t.Context(), session.NewSession("ultra-loaded", workDir, "test-model")); err != nil {
		t.Fatal(err)
	}
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.WorkDir, cfg.Session.Dir = workDir, sessionDir
		cfg.PermissionMode, cfg.Permissions.Mode = "ultrl", "ultrl"
		return cfg
	}, nil)
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": workDir}), &created); err != nil {
		t.Fatal(err)
	}
	assertCanonicalUltraState(t, created.Modes)
	var loaded SessionLoadResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionLoad, map[string]any{"sessionId": "ultra-loaded", "cwd": workDir}), &loaded); err != nil {
		t.Fatal(err)
	}
	assertCanonicalUltraState(t, loaded.Modes)
}

func TestACPSetModeAcceptsUltraAliasesAndRejectsInvalidModes(t *testing.T) {
	client := newACPHarness(t, func(_ context.Context, application *app.App, _, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		if application.Permissions.Mode() != permission.ModeUltra || application.Config.PermissionMode != permission.ModeUltra || application.Config.Permissions.Mode != permission.ModeUltra {
			t.Errorf("mode did not stay ultra: permissions=%s, config=%s/%s", application.Permissions.Mode(), application.Config.PermissionMode, application.Config.Permissions.Mode)
		}
		return agent.AgentResult{Output: "ok"}, nil
	})
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ultra", "ultrl", " ULTRA "} {
		mustCall(t, client, MethodSessionSetMode, map[string]any{"sessionId": created.SessionID, "modeId": "auto"})
		before := len(client.snapshot())
		mustCall(t, client, MethodSessionSetMode, map[string]any{"sessionId": created.SessionID, "modeId": mode})
		waitFor(t, func() bool {
			updates := client.snapshot()
			return len(updates) > before && hasMode(updates[before:], "ultra")
		})
	}
	for _, mode := range []string{"", " ", "unknown"} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		var result json.RawMessage
		err := client.conn.Call(ctx, MethodSessionSetMode, map[string]any{"sessionId": created.SessionID, "modeId": mode}, &result)
		cancel()
		if rpcErr, ok := err.(*jsonrpcError); !ok || rpcErr.Code != JSONRPCInvalidParams {
			t.Fatalf("set_mode(%q) error = %v, want invalid params", mode, err)
		}
	}
	mustCall(t, client, MethodSessionPrompt, map[string]any{"sessionId": created.SessionID, "prompt": []map[string]any{{"type": "text", "text": "verify"}}})
	for _, update := range client.snapshot() {
		if update.Update.SessionUpdate == "current_mode_update" && update.Update.CurrentModeID == "ultrl" {
			t.Fatal("legacy spelling leaked to ACP notifications")
		}
	}
}

func TestACPSessionModeSurvivesFeatureReload(t *testing.T) {
	cfg := config.Default()
	cfg.APIKey, cfg.WorkDir = "test", t.TempDir()
	cfg.Session.Enabled, cfg.Memory.Enabled, cfg.KnowledgeGraph.Enabled = false, false, false
	application, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	sess := &acpSession{cfg: cfg, application: application}
	if err := sess.setMode("ultrl"); err != nil {
		t.Fatal(err)
	}
	if sess.cfg.PermissionMode != permission.ModeUltra || sess.cfg.Permissions.Mode != permission.ModeUltra {
		t.Fatalf("session config is stale: %s/%s", sess.cfg.PermissionMode, sess.cfg.Permissions.Mode)
	}
	if err := application.ReloadFeatures(sess.cfg, nil); err != nil {
		t.Fatal(err)
	}
	if application.Permissions.Mode() != permission.ModeUltra || application.Config.PermissionMode != permission.ModeUltra || application.Config.Permissions.Mode != permission.ModeUltra {
		t.Fatal("feature reload reverted the permission mode")
	}
}

func TestACPModelInitiatedModeSwitchEmitsCanonicalUpdate(t *testing.T) {
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
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	mustCall(t, client, MethodSessionPrompt, map[string]any{"sessionId": created.SessionID, "prompt": []map[string]any{{"type": "text", "text": "switch to ultra"}}})
	waitFor(t, func() bool { return hasMode(client.snapshot(), "ultra") })
	sess := client.server.getSession(created.SessionID)
	sess.mu.Lock()
	reloadCfg := sess.cfg
	sess.mu.Unlock()
	if reloadCfg.PermissionMode != permission.ModeUltra || reloadCfg.Permissions.Mode != permission.ModeUltra {
		t.Fatal("model-initiated mode transition left the reload config stale")
	}
	if err := sess.application.ReloadFeatures(reloadCfg, nil); err != nil {
		t.Fatal(err)
	}
	if sess.application.Config.PermissionMode != permission.ModeUltra {
		t.Fatal("reload reverted a model-initiated mode transition")
	}
}
