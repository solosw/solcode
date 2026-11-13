package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolSearchConfigDefaultsAndPersistence(t *testing.T) {
	if !Default().ToolSearchEnabled() || !(Config{}).ToolSearchEnabled() {
		t.Fatal("search must default on")
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, enabled := range []bool{false, true, false} {
		if err := SaveLocalOverrides(path, map[string]any{"tool_search": map[string]any{"enabled": enabled}}); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ToolSearchEnabled() != enabled {
			t.Fatalf("reload enabled=%v want=%v", cfg.ToolSearchEnabled(), enabled)
		}
		overlay := filepath.Join(t.TempDir(), "overlay.json")
		if err := os.WriteFile(overlay, []byte(`{"tool_search":{},"max_turns":4}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := loadFile(&cfg, overlay); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Normalize(); err != nil {
			t.Fatal(err)
		}
		if cfg.ToolSearchEnabled() != enabled || cfg.MaxTurns != 4 {
			t.Fatal("partial overlay reset search setting")
		}
	}
}
