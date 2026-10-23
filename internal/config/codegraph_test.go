package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodeGraphConfigurationLayersAndPersistence(t *testing.T) {
	cfg := Default()
	if !cfg.CodeGraphEnabled() {
		t.Fatal("CodeGraph must default to enabled")
	}
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(file, []byte(`{"codegraph":{"enabled":false,"runtime_dir":"/dev/runtime"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodeGraphEnabled() || cfg.CodeGraph.RuntimeDir != "/dev/runtime" {
		t.Fatalf("%+v", cfg.CodeGraph)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.CodeGraphEnabled() || restored.CodeGraph.RuntimeDir != "/dev/runtime" {
		t.Fatal("lost CodeGraph settings")
	}
	// A later partial settings layer preserves the explicit disable flag.
	if err := applyJSONConfig(&cfg, []byte(`{"codegraph":{"runtime_dir":"/new/runtime"}}`)); err != nil {
		t.Fatal(err)
	}
	if cfg.CodeGraphEnabled() || cfg.CodeGraph.RuntimeDir != "/new/runtime" {
		t.Fatal("partial layer reset enable flag")
	}
}
