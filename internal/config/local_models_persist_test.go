package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalModelFilenameSaveReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "settings.json")
	old := map[string]any{"ort": map[string]any{"gpu": false}, "jev": map[string]any{"engine": "ort"}, "memory": map[string]any{"organizer": map[string]any{"model_path": "/old/Qwen.gguf", "processor": "cpu", "gpu_layers": 0}, "embedding": map[string]any{"model_path": "/old/Embedding.gguf", "gpu_layers": 0}}}
	raw, _ := json.Marshal(old)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	update := map[string]any{"jev": map[string]any{"type": "local", "model": "Kev.gguf"}, "memory": map[string]any{"organizer": map[string]any{"enabled": true, "model": "Qwen.gguf"}, "embedding": map[string]any{"type": "gguf", "model": "Embedding.gguf"}}}
	if err := SaveLocalOverrides(path, update); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.Organizer.ModelPath != LocalModelPath("Qwen.gguf") || cfg.Memory.Embedding.ModelPath != LocalModelPath("Embedding.gguf") || cfg.Jev.Engine != "gguf" {
		t.Fatalf("reload: %+v %+v %+v", cfg.Jev, cfg.Memory.Organizer, cfg.Memory.Embedding)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	memory := stored["memory"].(map[string]any)
	for _, key := range []string{"organizer", "embedding"} {
		fields := memory[key].(map[string]any)
		for _, obsolete := range []string{"model_path", "gpu_layers", "processor"} {
			if _, found := fields[obsolete]; found {
				t.Errorf("obsolete %s.%s persisted", key, obsolete)
			}
		}
	}
}
