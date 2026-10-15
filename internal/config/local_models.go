package config

import (
	"path/filepath"
	"runtime"
	"strings"
)

// ModelsDir is shared by every local model. Model settings contain filenames,
// never a working-directory-dependent path.
func ModelsDir() string { return filepath.Join(UserConfigDir(), "models") }

func localModelName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:") {
		return ""
	}
	if !strings.HasSuffix(strings.ToLower(name), ".gguf") {
		name += ".gguf"
	}
	return name
}
func legacyModelName(path string) string {
	path = strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}
func LocalModelPath(name string) string {
	name = localModelName(name)
	if name == "" {
		return ""
	}
	return filepath.Join(ModelsDir(), name)
}

// GPUProcessor selects the native package, not an inference fallback policy.
func GPUProcessor() string {
	if runtime.GOOS == "darwin" {
		return "metal"
	}
	return "cuda"
}

// NormalizeLocalModels derives runtime-only settings without requiring a valid
// chat provider (settings may be saved while providers are being edited).
func (cfg *Config) NormalizeLocalModels() {
	cfg.normalizeOrganizer()
	if cfg.JevType() == JevBackendLocal {
		cfg.normalizeJev()
	}
	if cfg.EmbeddingType() == EmbeddingBackendGGUF {
		cfg.normalizeEmbedding()
	}
}
