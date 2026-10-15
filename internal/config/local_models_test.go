package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalModelsShareDirectoryAndRequireGPU(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := Default()
	c.Jev = JevConfig{Enabled: true, Type: "local", Model: "Kev-4B-Q8_0.gguf", Engine: "ort", DType: "q4", ORTLib: "old.dll"}
	c.Memory.Organizer = OrganizerConfig{Enabled: true, ModelPath: "C:\\old\\Qwen.gguf", Processor: "cpu", GPULayers: 0}
	c.Memory.Embedding = EmbeddingConfig{Enabled: true, Type: "local", Model: "old-hint", ModelPath: "/old/Embedding.gguf", GPULayers: 0}
	c.NormalizeLocalModels()
	if c.Memory.Organizer.Model != "Qwen.gguf" || c.Memory.Organizer.ModelPath != LocalModelPath("Qwen.gguf") {
		t.Fatalf("organizer=%+v", c.Memory.Organizer)
	}
	if c.Memory.Embedding.Model != "Embedding.gguf" || c.Memory.Embedding.ModelPath != LocalModelPath("Embedding.gguf") {
		t.Fatalf("embedding=%+v", c.Memory.Embedding)
	}
	if c.Memory.Organizer.GPULayers != -1 || c.Memory.Embedding.GPULayers != -1 || c.Memory.Organizer.Processor != GPUProcessor() {
		t.Fatal("CPU/partial offload setting survived")
	}
	if c.Jev.ModelDir != "" || c.Jev.DType != "" || c.Jev.ORTLib != "" || c.Jev.Engine != "gguf" {
		t.Fatalf("jev=%+v", c.Jev)
	}
	if filepath.Dir(c.Memory.Organizer.ModelPath) != ModelsDir() || SharedEmbeddingModelDir() != ModelsDir() {
		t.Fatal("different model roots")
	}
}
func TestLocalModelPathRejectsDirectories(t *testing.T) {
	for _, s := range []string{"", "..", "../model.gguf", "/tmp/m.gguf", "C:\\m.gguf", "sub/m.gguf"} {
		if p := LocalModelPath(s); p != "" {
			t.Errorf("%q resolved outside filename contract: %q", s, p)
		}
	}
	if LocalModelPath("model") != filepath.Join(ModelsDir(), "model.gguf") {
		t.Fatal("stem not normalized")
	}
}
func TestSaveRemovesObsoleteONNXSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"ort":{"gpu":false},"jev":{"model_dir":"old","engine":"ort","custom":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalOverrides(p, map[string]any{"jev": map[string]any{"type": "local", "model": "Kev.gguf"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["ort"]; ok {
		t.Fatal("ORT persisted")
	}
	j := out["jev"].(map[string]any)
	if _, ok := j["engine"]; ok {
		t.Fatal("engine persisted")
	}
	if _, ok := j["model_dir"]; ok {
		t.Fatal("directory persisted")
	}
	if j["custom"] != true || j["model"] != "Kev.gguf" {
		t.Fatal(out)
	}
}
