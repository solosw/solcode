package workflowui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/config"
)

// The organizer block must be exposed to the UI. Unlike Jev/embedding it has no
// API mode, so the payload reports filesystem probes instead of keys.
func TestSettingsExposeMemoryOrganizerBlock(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	cfg := config.Default()
	cfg.WorkDir = dir
	cfg.Memory.Organizer.Enabled = true
	cfg.Memory.Organizer.ModelPath = modelPath
	cfg.Memory.Organizer.ContextSize = 4096
	cfg.Memory.Organizer.GPULayers = 12
	cfg.Normalize()

	_, url, _ := settingsServer(t, cfg)
	body := decodeSettings(t, url)

	org, ok := body["memory_organizer"].(map[string]any)
	if !ok {
		t.Fatalf("memory_organizer block missing from settings payload: %#v", body)
	}
	if got := org["enabled"]; got != true {
		t.Fatalf("enabled = %#v, want true", got)
	}
	if got := org["runtime"]; got != "yzma" {
		t.Fatalf("runtime = %#v, want yzma", got)
	}
	if got := org["model_path"]; got != modelPath {
		t.Fatalf("model_path = %#v, want %q", got, modelPath)
	}
	if got := org["model_present"]; got != true {
		t.Fatalf("model_present = %#v, want true for an existing file", got)
	}
	if got := org["library_present"]; got != false {
		t.Fatalf("library_present = %#v, want false when no library is installed", got)
	}
	if got := org["context_size"]; got != float64(4096) {
		t.Fatalf("context_size = %#v, want 4096", got)
	}
	if got := org["gpu_layers"]; got != float64(12) {
		t.Fatalf("gpu_layers = %#v, want 12", got)
	}
}

// A partial update must not reset the rest of the organizer configuration.
func TestSettingsMemoryOrganizerPartialUpdatePreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.WorkDir = dir
	cfg.Memory.Organizer.Enabled = true
	cfg.Memory.Organizer.ModelPath = filepath.Join(dir, "model.gguf")
	cfg.Memory.Organizer.LibDir = filepath.Join(dir, "libs")
	cfg.Memory.Organizer.ContextSize = 4096
	cfg.Normalize()

	_, url, applied := settingsServer(t, cfg)

	// Toggle only Enabled; everything else must survive.
	res := postSettings(t, url, map[string]any{"memory_organizer_enabled": false})
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("POST settings status = %d, want 200", res.StatusCode)
	}

	if applied.Memory.Organizer.Enabled {
		t.Fatal("enabled should have been turned off")
	}
	if got := applied.Memory.Organizer.ModelPath; got != filepath.Join(dir, "model.gguf") {
		t.Fatalf("model path was reset to %q", got)
	}
	if got := applied.Memory.Organizer.ContextSize; got != 4096 {
		t.Fatalf("context size was reset to %d", got)
	}
	if got := applied.Memory.Organizer.LibDir; got != filepath.Join(dir, "libs") {
		t.Fatalf("lib dir was reset to %q", got)
	}
}

func TestSettingsMemoryOrganizerAppliesAllFields(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Normalize()

	_, url, applied := settingsServer(t, cfg)

	res := postSettings(t, url, map[string]any{
		"memory_organizer_enabled":           true,
		"memory_organizer_runtime":           "yzma",
		"memory_organizer_model_path":        "~/models/local.gguf",
		"memory_organizer_lib_dir":           "~/libs",
		"memory_organizer_processor":         "vulkan",
		"memory_organizer_context_size":      16384,
		"memory_organizer_threads":           6,
		"memory_organizer_gpu_layers":        -1,
		"memory_organizer_max_output_tokens": 2048,
		"memory_organizer_temperature":       0.4,
		"memory_organizer_timeout_sec":       300,
	})
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("POST settings status = %d, want 200", res.StatusCode)
	}

	// postSettings intentionally does not Normalize (see its comment); the real
	// pipeline normalizes before applying runtime changes. Mirror that here.
	applied.Normalize()
	org := applied.Memory.Organizer
	if !org.Enabled {
		t.Fatal("enabled was not applied")
	}
	if org.Processor != "vulkan" {
		t.Fatalf("processor = %q, want vulkan", org.Processor)
	}
	if org.ContextSize != 16384 {
		t.Fatalf("context size = %d, want 16384", org.ContextSize)
	}
	if org.Threads != 6 {
		t.Fatalf("threads = %d, want 6", org.Threads)
	}
	if org.GPULayers != -1 {
		t.Fatalf("gpu layers = %d, want -1", org.GPULayers)
	}
	if org.MaxOutputTokens != 2048 {
		t.Fatalf("max output tokens = %d, want 2048", org.MaxOutputTokens)
	}
	if org.Temperature != 0.4 {
		t.Fatalf("temperature = %v, want 0.4", org.Temperature)
	}
	if org.TimeoutSec != 300 {
		t.Fatalf("timeout = %d, want 300", org.TimeoutSec)
	}
	if org.Runtime != config.OrganizerRuntimeYzma {
		t.Fatalf("runtime = %q, want yzma", org.Runtime)
	}
	// Normalize expands ~ into the user's home directory.
	if org.ModelPath == "" || org.ModelPath[0] == '~' {
		t.Fatalf("model path was not expanded: %q", org.ModelPath)
	}
	if org.LibDir == "" || org.LibDir[0] == '~' {
		t.Fatalf("lib dir was not expanded: %q", org.LibDir)
	}
}

// The library probe must find a real llama.cpp shared library by name shape
// rather than requiring a specific version.
func TestMemoryOrganizerLibraryProbe(t *testing.T) {
	dir := t.TempDir()
	if memoryOrganizerLibraryPresent(dir) {
		t.Fatal("an empty directory must not report a library")
	}

	// A record alone is not enough; the library file must exist.
	if err := os.WriteFile(filepath.Join(dir, "yzma-install.json"), []byte(`{"version":"b9433"}`), 0o644); err != nil {
		t.Fatalf("write record: %v", err)
	}
	if memoryOrganizerLibraryPresent(dir) {
		t.Fatal("an install record without a library must not report present")
	}
	if got := memoryOrganizerLibraryVersion(dir); got != "b9433" {
		t.Fatalf("version = %q, want b9433", got)
	}

	// Any platform's library filename shape should be detected.
	for _, name := range []string{"llama.dll", "libllama.so", "libllama.so.1.30.0", "libllama.dylib"} {
		libDir := filepath.Join(t.TempDir(), "lib")
		if err := os.MkdirAll(libDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(libDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if !memoryOrganizerLibraryPresent(libDir) {
			t.Fatalf("library %q was not detected", name)
		}
	}

	// Unrelated files must not be mistaken for the library.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "ggml.dll"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write ggml: %v", err)
	}
	if memoryOrganizerLibraryPresent(other) {
		t.Fatal("a non-llama file must not be reported as the llama.cpp library")
	}
}

func TestMemoryOrganizerModelProbe(t *testing.T) {
	if memoryOrganizerModelPresent("") {
		t.Fatal("an empty path must not report a model")
	}
	if memoryOrganizerModelPresent(t.TempDir()) {
		t.Fatal("a directory must not report a model")
	}
	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	if !memoryOrganizerModelPresent(path) {
		t.Fatal("an existing file must report a model")
	}
}

// Disabling the organizer must not require any of the model fields to be set.
func TestSettingsMemoryOrganizerDisabledByDefault(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Normalize()

	_, url, _ := settingsServer(t, cfg)
	body := decodeSettings(t, url)

	org, ok := body["memory_organizer"].(map[string]any)
	if !ok {
		t.Fatal("memory_organizer block missing from settings payload")
	}
	if got := org["enabled"]; got != false {
		t.Fatalf("enabled = %#v, want false by default", got)
	}
}
