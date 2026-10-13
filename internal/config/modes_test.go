package config

import (
	"testing"

	"github.com/solosw/solcode/internal/permission"
)

func TestPermissionModeSettingsAndLegacyUltra(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       permission.Mode
	}{
		{"top-level ultra", `{"permission_mode":"ultra"}`, permission.ModeUltra},
		{"top-level legacy", `{"permission_mode":"ultrl"}`, permission.ModeUltra},
		{"nested ultra", `{"permissions":{"mode":"ultra"}}`, permission.ModeUltra},
		{"nested legacy", `{"permissions":{"mode":"ultrl"}}`, permission.ModeUltra},
		{"nested takes precedence", `{"permission_mode":"plan","permissions":{"mode":"ultrl"}}`, permission.ModeUltra},
		{"nested auto wins", `{"permission_mode":"ultra","permissions":{"mode":"auto"}}`, permission.ModeAuto},
		{"allowlist retains top-level mode", `{"permission_mode":"ultrl","permissions":{"allow":["View"]}}`, permission.ModeUltra},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			if err := applyJSONConfig(&cfg, []byte(tc.body)); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Normalize(); err != nil {
				t.Fatal(err)
			}
			if cfg.PermissionMode != tc.want || cfg.Permissions.Mode != tc.want {
				t.Fatalf("mode = %s/%s, want %s", cfg.PermissionMode, cfg.Permissions.Mode, tc.want)
			}
		})
	}
}

func TestNormalizeUsesTopLevelModeWhenNestedModeIsUnset(t *testing.T) {
	cfg := Default()
	cfg.PermissionMode, cfg.Permissions.Mode = "ultrl", ""
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.PermissionMode != permission.ModeUltra || cfg.Permissions.Mode != permission.ModeUltra {
		t.Fatalf("mode = %s/%s, want ultra", cfg.PermissionMode, cfg.Permissions.Mode)
	}
}
