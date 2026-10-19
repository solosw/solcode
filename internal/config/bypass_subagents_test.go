package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/permission"
)

func TestBypassSubagentsConfigDefaultsAndLayering(t *testing.T) {
	cfg := Default()
	if cfg.Permissions.BypassSubagents {
		t.Fatal("bypass sub-agents must default to disabled")
	}
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"permission_mode":"bypass"}`, false},
		{`{"permissions":{"bypass_subagents":true}}`, true},
		{`{"permissions":{"allow":["Task"]}}`, true},
		{`{"permissions":{"bypass_subagents":false}}`, false},
		{`{"permissions":{"mode":"ultra"}}`, false},
	} {
		if err := applyJSONConfig(&cfg, []byte(tc.body)); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Normalize(); err != nil {
			t.Fatal(err)
		}
		if cfg.Permissions.BypassSubagents != tc.want {
			t.Fatalf("after %s: enabled = %v, want %v", tc.body, cfg.Permissions.BypassSubagents, tc.want)
		}
	}
	if err := applyJSONConfig(&cfg, []byte(`{"permissions":{"bypass_subagents":"true"}}`)); err == nil {
		t.Fatal("invalid bypass_subagents type accepted")
	}
}

func TestBypassSubagentsPersistsFalseWithoutResettingPermissions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "settings.local.json")
	if err := os.WriteFile(path, []byte(`{"permissions":{"mode":"bypass","allow":["View"],"allow_bash":["git status"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		if err := SaveLocalOverrides(path, map[string]any{"permissions": map[string]any{"bypass_subagents": enabled}}); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Permissions.BypassSubagents != enabled {
			t.Fatalf("reloaded enabled = %v, want %v", cfg.Permissions.BypassSubagents, enabled)
		}
		if cfg.PermissionMode != permission.ModeBypass || len(cfg.Permissions.Allow) != 1 || cfg.Permissions.Allow[0] != "View" || len(cfg.Permissions.AllowBash) != 1 || cfg.Permissions.AllowBash[0] != "git status" {
			t.Fatalf("toggle reset unrelated permissions: %+v", cfg.Permissions)
		}
	}
}
