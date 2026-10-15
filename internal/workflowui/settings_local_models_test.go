package workflowui

import (
	"github.com/solosw/solcode/internal/config"
	"os"
	"strings"
	"testing"
)

func TestLocalModelFilenameSettings(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	_, url, applied := settingsServer(t, cfg)
	r := postSettings(t, url, map[string]any{"jev_type": "local", "jev_model": "Kev-4B-Q8_0.gguf", "embedding_type": "gguf", "embedding_model": "embeddinggemma-2-BF16.gguf", "memory_organizer_model": "Qwen3.5-0.8B-BF16.gguf", "memory_organizer_processor": "cpu", "embedding_gpu_layers": 0})
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if applied.Memory.Organizer.ModelPath != config.LocalModelPath("Qwen3.5-0.8B-BF16.gguf") || applied.Memory.Embedding.ModelPath != config.LocalModelPath("embeddinggemma-2-BF16.gguf") {
		t.Fatal("filenames not resolved in shared models directory")
	}
	if applied.Memory.Embedding.GPULayers != -1 || applied.Memory.Organizer.GPULayers != -1 {
		t.Fatal("CPU setting survived")
	}
}
func TestSettingsHaveNoLocalHardwareControls(t *testing.T) {
	raw, err := os.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, id := range []string{"set-jev-model-dir", "set-jev-dtype", "set-jev-engine", "set-ort-gpu", "set-ort-cuda-device-id", "set-memory-organizer-processor", "set-memory-organizer-gpu-layers", "set-memory-organizer-lib-dir"} {
		if strings.Contains(s, `id="`+id+`"`) {
			t.Errorf("obsolete control remains: %s", id)
		}
	}
	if !strings.Contains(s, "~/.solcode/models") || !strings.Contains(s, "Advanced generation settings") {
		t.Fatal("missing shared-directory hint / advanced disclosure")
	}
}
