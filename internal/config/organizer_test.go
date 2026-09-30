package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeOrganizerDefaults(t *testing.T) {
	cfg := Default()
	cfg.Normalize()

	org := cfg.Memory.Organizer
	if org.Runtime != OrganizerRuntimeYzma {
		t.Fatalf("runtime = %q, want %q", org.Runtime, OrganizerRuntimeYzma)
	}
	if org.Processor != "cpu" {
		t.Fatalf("processor = %q, want cpu", org.Processor)
	}
	if org.GPULayers != 0 {
		t.Fatalf("gpu layers = %d, want 0 on cpu", org.GPULayers)
	}
	if org.ContextSize != 16384 {
		t.Fatalf("context size = %d, want 16384", org.ContextSize)
	}
	if org.MaxOutputTokens != 800 {
		t.Fatalf("max output tokens = %d, want 800", org.MaxOutputTokens)
	}
	if org.TimeoutSec != 180 {
		t.Fatalf("timeout = %d, want 180", org.TimeoutSec)
	}
}

func TestNormalizeOrganizerCUDADefaultsAllGPULayers(t *testing.T) {
	cfg := Default()
	cfg.Memory.Organizer.Processor = "cuda"
	// Leave GPULayers at the zero value: cuda should offload everything.
	cfg.Normalize()
	if cfg.Memory.Organizer.GPULayers != -1 {
		t.Fatalf("gpu layers = %d, want -1 when processor is cuda and unset", cfg.Memory.Organizer.GPULayers)
	}

	cfg = Default()
	cfg.Memory.Organizer.Processor = "cuda"
	cfg.Memory.Organizer.GPULayers = 12
	cfg.Normalize()
	if cfg.Memory.Organizer.GPULayers != 12 {
		t.Fatalf("gpu layers = %d, want the explicit 12 to stick", cfg.Memory.Organizer.GPULayers)
	}
}

func TestNormalizeOrganizerBoundsValues(t *testing.T) {
	cfg := Default()
	cfg.Memory.Organizer.ContextSize = 10_000_000
	cfg.Memory.Organizer.MaxOutputTokens = 100_000
	cfg.Memory.Organizer.Temperature = 9
	cfg.Memory.Organizer.TimeoutSec = 10_000
	cfg.Memory.Organizer.Processor = "Silly"
	cfg.Normalize()

	org := cfg.Memory.Organizer
	if org.ContextSize != 16384 {
		t.Fatalf("context size = %d, want clamped to 16384", org.ContextSize)
	}
	if org.MaxOutputTokens != 2048 {
		t.Fatalf("max output tokens = %d, want clamped to 2048", org.MaxOutputTokens)
	}
	if org.Temperature != 2 {
		t.Fatalf("temperature = %v, want clamped to 2", org.Temperature)
	}
	if org.TimeoutSec != 900 {
		t.Fatalf("timeout = %d, want clamped to 900", org.TimeoutSec)
	}
	if org.Processor != "cpu" {
		t.Fatalf("processor = %q, want cpu fallback", org.Processor)
	}
}

func TestOrganizerEnabledRequiresModelPath(t *testing.T) {
	cfg := Default()
	cfg.Memory.Organizer.Enabled = true
	cfg.Normalize()
	if cfg.OrganizerEnabled() {
		t.Fatal("organizer must stay disabled without a model path")
	}

	cfg.Memory.Organizer.ModelPath = filepath.Join(t.TempDir(), "model.gguf")
	cfg.Normalize()
	// A path is required, but the file need not exist for Enabled to be true:
	// solcode still starts and the worker retries once the model is placed.
	if !cfg.OrganizerEnabled() {
		t.Fatal("organizer should be enabled once a model path is set")
	}
}

func TestOrganizerDisabledByDefault(t *testing.T) {
	cfg := Default()
	cfg.Normalize()
	if cfg.OrganizerEnabled() {
		t.Fatal("organizer must be off by default")
	}
}

func TestOrganizerTypeNormalizesUnknownBackend(t *testing.T) {
	cfg := Default()
	cfg.Memory.Organizer.Runtime = "some-future-runtime"
	cfg.Normalize()
	if got := cfg.OrganizerType(); got != DefaultOrganizerRuntime {
		t.Fatalf("OrganizerType() = %q, want %q", got, DefaultOrganizerRuntime)
	}
}

func TestOrganizerLibDirPrecedence(t *testing.T) {
	cfg := Default()
	cfg.Normalize()
	custom := filepath.Join(t.TempDir(), "custom-libs")
	cfg.Memory.Organizer.LibDir = custom
	cfg.Normalize()
	if got := cfg.OrganizerLibDir(); got != custom {
		t.Fatalf("OrganizerLibDir() = %q, want %q", got, custom)
	}
}

func TestOrganizerLibDirDefaultIsUnderUserConfig(t *testing.T) {
	t.Setenv("YZMA_LIB", "")
	cfg := Default()
	cfg.Normalize()
	got := cfg.OrganizerLibDir()
	if !strings.HasSuffix(filepath.ToSlash(got), "lib/llama") {
		t.Fatalf("OrganizerLibDir() = %q, want a .../lib/llama path", got)
	}
}

func TestOrganizerLibDirUsesYzmaLibEnv(t *testing.T) {
	env := filepath.Join(t.TempDir(), "from-env")
	t.Setenv("YZMA_LIB", env)
	cfg := Default()
	cfg.Normalize()
	if got := cfg.OrganizerLibDir(); got != env {
		t.Fatalf("OrganizerLibDir() = %q, want the YZMA_LIB value %q", got, env)
	}
}

func TestOrganizerModelPathExpandsHome(t *testing.T) {
	cfg := Default()
	cfg.Memory.Organizer.ModelPath = "~/models/local.gguf"
	cfg.Normalize()
	path := cfg.Memory.Organizer.ModelPath
	if strings.HasPrefix(path, "~") {
		t.Fatalf("model path was not expanded: %q", path)
	}
	if !strings.HasSuffix(filepath.ToSlash(path), "models/local.gguf") {
		t.Fatalf("model path = %q, want it to end with models/local.gguf", path)
	}
}
