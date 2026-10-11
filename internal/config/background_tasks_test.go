package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackgroundTasksDefaultsAndConfiguration(t *testing.T) {
	defaults := Default().BackgroundTasks
	if !defaults.Enabled || defaults.YieldAfterMS != 10_000 {
		t.Fatalf("defaults = %#v", defaults)
	}
	for _, tc := range []struct {
		name, body string
		enabled    bool
		ms         int
	}{
		{"default", `{}`, true, 10_000},
		{"custom yield", `{"background_tasks":{"yield_after_ms":250}}`, true, 250},
		{"disabled", `{"background_tasks":{"enabled":false}}`, false, 10_000},
		{"normalize nonpositive", `{"background_tasks":{"yield_after_ms":-1}}`, true, 10_000},
		{"clamp", `{"background_tasks":{"yield_after_ms":100000000}}`, true, 86_400_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.BackgroundTasks.Enabled != tc.enabled || cfg.BackgroundTasks.YieldAfterMS != tc.ms {
				t.Fatalf("config = %#v", cfg.BackgroundTasks)
			}
		})
	}
}
