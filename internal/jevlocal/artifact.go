package jevlocal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Family identifiers for local System-One ONNX layouts.
const (
	FamilyOpenJev = "open-jev"
	FamilyLaya    = "laya"
)

// OpenJevConfig is the on-disk open_jev_config.json contract.
type OpenJevConfig struct {
	Pool           string   `json:"pool"`
	Temperature    float64  `json:"temperature"`
	Markers        []string `json:"markers"`
	MaxStateTokens int      `json:"max_state_tokens"`
	MaxLen         int      `json:"max_len"`
	Hidden         int      `json:"hidden"`
	BaseModel      string   `json:"base_model"`
}

// LayaConfig is the on-disk rl_agent_config.json contract (subset used at Ask).
type LayaConfig struct {
	Encoder              string             `json:"encoder"`
	MaxLen               int                `json:"max_len"`
	HeadMaxLen           int                `json:"head_max_len"`
	MaxPrefixes          int                `json:"max_prefixes"`
	Temperature          []float64          `json:"temperature"`
	TemperatureByOptions map[string]float64 `json:"temperature_by_options"`
}

// Artifacts describes a resolved local model directory.
type Artifacts struct {
	Dir        string
	Family     string
	DType      string
	ModelID    string
	ONNXPath   string
	Config     OpenJevConfig // populated for open-jev
	LayaConfig LayaConfig    // populated for laya
}

// ArtifactsPresent reports whether modelDir looks like a usable local root
// for the requested dtype (OpenJev or Laya).
func ArtifactsPresent(modelDir, dtype string) bool {
	_, err := ResolveArtifacts(modelDir, dtype, "")
	return err == nil
}

// DetectFamily classifies a model directory without fully resolving ONNX.
func DetectFamily(modelDir string) (string, error) {
	dir := strings.TrimSpace(modelDir)
	if dir == "" {
		return "", fmt.Errorf("jev local model_dir is empty")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("jev local model_dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("jev local model_dir is not a directory: %s", dir)
	}
	switch {
	case fileExists(filepath.Join(dir, "open_jev_config.json")):
		return FamilyOpenJev, nil
	case fileExists(filepath.Join(dir, "rl_agent_config.json")):
		return FamilyLaya, nil
	default:
		return "", fmt.Errorf("jev local model_dir: unknown family (need open_jev_config.json or rl_agent_config.json)")
	}
}

// ResolveArtifacts validates and loads local layout metadata.
func ResolveArtifacts(modelDir, dtype, modelID string) (Artifacts, error) {
	family, err := DetectFamily(modelDir)
	if err != nil {
		return Artifacts{}, err
	}
	dir := strings.TrimSpace(modelDir)
	dtype = strings.ToLower(strings.TrimSpace(dtype))

	id := strings.TrimSpace(modelID)
	if id == "" {
		id = filepath.Base(dir)
	}

	switch family {
	case FamilyOpenJev:
		return resolveOpenJevArtifacts(dir, dtype, id)
	case FamilyLaya:
		return resolveLayaArtifacts(dir, dtype, id)
	default:
		return Artifacts{}, fmt.Errorf("unsupported local jev family %q", family)
	}
}

func resolveOpenJevArtifacts(dir, dtype, id string) (Artifacts, error) {
	if dtype == "" {
		dtype = "q4"
	}
	cfgPath := filepath.Join(dir, "open_jev_config.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return Artifacts{}, fmt.Errorf("open_jev_config.json: %w", err)
	}
	var cfg OpenJevConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Artifacts{}, fmt.Errorf("decode open_jev_config.json: %w", err)
	}
	if cfg.Temperature <= 0 {
		cfg.Temperature = 1.05
	}
	if cfg.MaxLen <= 0 {
		cfg.MaxLen = 512
	}
	if cfg.MaxStateTokens <= 0 {
		cfg.MaxStateTokens = 256
	}
	if len(cfg.Markers) == 0 {
		cfg.Markers = []string{"[STATE]", "[Q]", "[OPT]"}
	}
	if !hasTokenizer(dir) {
		return Artifacts{}, fmt.Errorf("jev local model_dir missing tokenizer.json or spm.model")
	}
	onnxPath, err := resolveONNXPath(dir, dtype)
	if err != nil {
		return Artifacts{}, err
	}
	return Artifacts{
		Dir:      dir,
		Family:   FamilyOpenJev,
		DType:    dtype,
		ModelID:  id,
		ONNXPath: onnxPath,
		Config:   cfg,
	}, nil
}

func resolveLayaArtifacts(dir, dtype, id string) (Artifacts, error) {
	// Laya ships a single root model.onnx; dtype is informational.
	if dtype == "" {
		dtype = "fp32"
	}
	cfgPath := filepath.Join(dir, "rl_agent_config.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return Artifacts{}, fmt.Errorf("rl_agent_config.json: %w", err)
	}
	var cfg LayaConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Artifacts{}, fmt.Errorf("decode rl_agent_config.json: %w", err)
	}
	if cfg.MaxLen <= 0 {
		cfg.MaxLen = 512
	}
	if cfg.HeadMaxLen <= 0 {
		cfg.HeadMaxLen = 192
	}
	if len(cfg.Temperature) == 0 {
		cfg.Temperature = []float64{1, 1, 1}
	}
	for len(cfg.Temperature) < 3 {
		cfg.Temperature = append(cfg.Temperature, 1)
	}
	if cfg.TemperatureByOptions == nil {
		cfg.TemperatureByOptions = map[string]float64{}
	}
	if !fileExists(filepath.Join(dir, "tokenizer.json")) {
		return Artifacts{}, fmt.Errorf("laya model_dir missing tokenizer.json")
	}
	onnxPath, err := resolveONNXPath(dir, dtype)
	if err != nil {
		return Artifacts{}, err
	}
	return Artifacts{
		Dir:        dir,
		Family:     FamilyLaya,
		DType:      dtype,
		ModelID:    id,
		ONNXPath:   onnxPath,
		LayaConfig: cfg,
	}, nil
}

func hasTokenizer(dir string) bool {
	if fileExists(filepath.Join(dir, "tokenizer.json")) {
		return true
	}
	return fileExists(filepath.Join(dir, "spm.model"))
}

func resolveONNXPath(dir, dtype string) (string, error) {
	candidates := []string{
		filepath.Join(dir, "onnx", "model_"+dtype+".onnx"),
		filepath.Join(dir, "onnx", "model.onnx"),
		filepath.Join(dir, "model_"+dtype+".onnx"),
		filepath.Join(dir, "model.onnx"),
	}
	for _, path := range candidates {
		if fileExists(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("jev local onnx graph for dtype %q not found under %s", dtype, dir)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
