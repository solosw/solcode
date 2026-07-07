package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/solosw/codeplus-agent/internal/anthropic"
	"github.com/solosw/codeplus-agent/internal/hook"
	"github.com/solosw/codeplus-agent/internal/permission"
)

const (
	configDirName     = ".agentcode"
	settingsFileName  = "settings.json"
	settingsLocalName = "settings.local.json"
)

func UserConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, configDirName)
}

func UserStateDir() string {
	return UserConfigDir()
}

func DefaultRuntimeSettingsPath() string {
	return filepath.Join(UserConfigDir(), settingsLocalName)
}

func ProjectConfigDir(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, configDirName)
}

func DefaultSkillDirs(workDir string) []string {
	paths := []string{filepath.Join(UserConfigDir(), "skills")}
	legacyPaths := []string{filepath.Join(UserConfigDir(), "my-skill")}
	if projectDir := ProjectConfigDir(workDir); projectDir != "" {
		paths = append(paths, filepath.Join(projectDir, "skills"))
		legacyPaths = append(legacyPaths, filepath.Join(projectDir, "my-skill"))
	}
	return uniqueNonEmpty(append(paths, legacyPaths...))
}

func projectSubDir(workDir string) string {
	if workDir == "" {
		return ""
	}
	// Convert the full project path into a filesystem-safe directory name so different
	// projects with the same basename do not collide.
	var b strings.Builder
	for _, r := range filepath.ToSlash(workDir) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func DefaultSessionDir(workDir string) string {
	if sub := projectSubDir(workDir); sub != "" {
		return filepath.Join(UserStateDir(), sub, "sessions")
	}
	return filepath.Join(UserStateDir(), "sessions")
}

func DefaultMemoryDir(workDir string) string {
	if sub := projectSubDir(workDir); sub != "" {
		return filepath.Join(UserStateDir(), sub, "memories")
	}
	return filepath.Join(UserStateDir(), "memories")
}

func DefaultTodoPath(workDir string) string {
	if sub := projectSubDir(workDir); sub != "" {
		return filepath.Join(UserStateDir(), sub, "todos.json")
	}
	return filepath.Join(UserStateDir(), "todos.json")
}

type Config struct {
	APIKey           string            `json:"api_key"`
	BaseURL          string            `json:"base_url"`
	Model            string            `json:"model"`
	MaxContextTokens int64             `json:"max_context_tokens,omitempty"`
	MaxTokens        int64             `json:"max_tokens"`
	SystemPrompt     string            `json:"system_prompt"`
	WorkDir          string            `json:"work_dir"`
	MaxTurns         int               `json:"max_turns"`
	Stream           bool              `json:"stream"`
	Thinking         bool              `json:"thinking"`
	ThinkingText     bool              `json:"thinking_text"`
	Effort           string            `json:"effort"`
	PermissionMode   permission.Mode   `json:"permission_mode"`
	Permissions      permission.Config `json:"permissions,omitempty"`
	Hooks            hook.Config       `json:"hooks,omitempty"`
	Skills           SkillsConfig      `json:"skills,omitempty"`
	MCP              MCPConfig         `json:"mcp,omitempty"`
	MCPServers       []MCPServerConfig `json:"mcp_servers,omitempty"`
	Session          SessionConfig     `json:"session,omitempty"`
	Memory           MemoryConfig      `json:"memory,omitempty"`

	Provider  string           `json:"provider,omitempty"`
	Providers []ProviderConfig `json:"providers,omitempty"`
}

type SkillsConfig struct {
	Paths    []string `json:"paths,omitempty"`
	Enabled  []string `json:"enabled,omitempty"`
	Disabled []string `json:"disabled,omitempty"`
}

type MCPConfig struct {
	Servers []MCPServerConfig `json:"servers,omitempty"`
}

type SessionConfig struct {
	Enabled        bool   `json:"enabled,omitempty"`
	Persist        bool   `json:"persist,omitempty"`
	Dir            string `json:"dir,omitempty"`
	DefaultSession string `json:"default_session,omitempty"`
}

type MemoryConfig struct {
	Enabled                  bool    `json:"enabled,omitempty"`
	Dir                      string  `json:"dir,omitempty"`
	MaxRecentTurns           int     `json:"max_recent_turns,omitempty"`
	SummaryThresholdTokens   int     `json:"summary_threshold_tokens,omitempty"`
	CompactionTriggerPercent int     `json:"compaction_trigger_percent,omitempty"`
	CompactionTargetPercent  int     `json:"compaction_target_percent,omitempty"`
	RetrievalLimit           int     `json:"retrieval_limit,omitempty"`
	RetrievalM2Limit         int     `json:"retrieval_m2_limit,omitempty"`
	RetrievalM3Limit         int     `json:"retrieval_m3_limit,omitempty"`
	RetrievalM4Limit         int     `json:"retrieval_m4_limit,omitempty"`
	RetrievalM5Limit         int     `json:"retrieval_m5_limit,omitempty"`
	TierM1TTLHours           int     `json:"tier_m1_ttl_hours,omitempty"`
	TierM2TTLHours           int     `json:"tier_m2_ttl_hours,omitempty"`
	PromotionAccessThreshold int     `json:"promotion_access_threshold,omitempty"`
	PromotionConfidence      float64 `json:"promotion_confidence,omitempty"`
}

type MCPServerConfig struct {
	Name      string            `json:"name,omitempty"`
	Transport string            `json:"transport,omitempty"`
	Type      string            `json:"type,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Disabled  bool              `json:"disabled,omitempty"`
}

type ProviderConfig struct {
	Name       string        `json:"name"`
	Type       string        `json:"type"`
	APIKey     string        `json:"api_key,omitempty"`
	APIKeyEnv  string        `json:"api_key_env,omitempty"`
	BaseURL    string        `json:"base_url,omitempty"`
	BaseURLEnv string        `json:"base_url_env,omitempty"`
	Models     []ModelConfig `json:"models"`
}

type ResolvedProvider struct {
	Name    string
	Type    string
	APIKey  string
	BaseURL string
}

type ModelConfig struct {
	Name             string                     `json:"name"`
	ID               string                     `json:"id"`
	DisplayName      string                     `json:"display_name,omitempty"`
	Provider         string                     `json:"provider,omitempty"`
	Default          bool                       `json:"default,omitempty"`
	MaxContextTokens int64                      `json:"max_context_tokens,omitempty"`
	MaxTokens        *int64                     `json:"max_tokens,omitempty"`
	MaxTurns         *int                       `json:"max_turns,omitempty"`
	Thinking         *bool                      `json:"thinking,omitempty"`
	ThinkingText     *bool                      `json:"thinking_text,omitempty"`
	Effort           string                     `json:"effort,omitempty"`
	APIParams        map[string]json.RawMessage `json:"api_params,omitempty"`
}

func Default() Config {
	wd, _ := os.Getwd()
	return Config{
		APIKey:           os.Getenv("ANTHROPIC_API_KEY"),
		BaseURL:          os.Getenv("ANTHROPIC_BASE_URL"),
		Model:            anthropic.DefaultModel,
		MaxContextTokens: 200_000,
		MaxTokens:        64_000,
		WorkDir:          wd,
		MaxTurns:         0,
		Stream:           true,
		Thinking:         true,
		ThinkingText:     false,
		Effort:           "high",
		PermissionMode:   permission.ModeAuto,
		Permissions: permission.Config{
			Mode: permission.ModeAuto,
		},
		Session: SessionConfig{
			Enabled:        true,
			Persist:        true,
			DefaultSession: "main",
		},
		Memory: MemoryConfig{
			Enabled:                  true,
			MaxRecentTurns:           20,
			SummaryThresholdTokens:   60_000,
			CompactionTriggerPercent: 85,
			CompactionTargetPercent:  50,
			RetrievalLimit:           8,
			RetrievalM2Limit:         4,
			RetrievalM3Limit:         3,
			RetrievalM4Limit:         3,
			RetrievalM5Limit:         2,
			TierM1TTLHours:           12,
			TierM2TTLHours:           72,
			PromotionAccessThreshold: 3,
			PromotionConfidence:      0.75,
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		if err := loadFile(&cfg, path); err != nil {
			return cfg, err
		}
		if err := cfg.Normalize(); err != nil {
			return cfg, err
		}
		return cfg, nil
	}

	if err := initializeDefaultSettingsFile(cfg.WorkDir); err != nil {
		return cfg, err
	}
	for _, candidate := range discoverDefaultPaths() {
		if err := loadOptionalFile(&cfg, candidate); err != nil {
			return cfg, err
		}
	}

	if err := cfg.Normalize(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func initializeDefaultSettingsFile(workDir string) error {
	for _, candidate := range discoverDefaultPaths() {
		if _, err := os.Stat(candidate); err == nil {
			return nil
		}
	}
	path := filepath.Join(UserConfigDir(), settingsFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory %q: %w", filepath.Dir(path), err)
	}
	payload := defaultSettingsPayload(workDir)
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode default settings %q: %w", path, err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return fmt.Errorf("write default settings %q: %w", path, err)
	}
	return nil
}

func defaultSettingsPayload(workDir string) map[string]any {
	maxTokens := int64(64_000)
	maxTurns := 0
	return map[string]any{
		"provider": "anthropic",
		"providers": []ProviderConfig{{
			Name:      "anthropic",
			Type:      "anthropic",
			APIKeyEnv: "ANTHROPIC_API_KEY",
			Models: []ModelConfig{{
				Name:             "default",
				ID:               anthropic.DefaultModel,
				DisplayName:      anthropic.DefaultModel,
				Default:          true,
				MaxContextTokens: 200_000,
				MaxTokens:        &maxTokens,
				MaxTurns:         &maxTurns,
				Effort:           "high",
			}},
		}},
		"work_dir": workDir,
	}
}

func (cfg *Config) Normalize() error {
	cfg.PermissionMode = permission.NormalizeMode(cfg.PermissionMode)
	cfg.Permissions.Mode = permission.NormalizeMode(cfg.Permissions.Mode)
	if cfg.Permissions.Mode == "" {
		cfg.Permissions.Mode = cfg.PermissionMode
	}
	if cfg.Permissions.Mode == "" {
		cfg.Permissions.Mode = permission.ModeAuto
	}
	cfg.PermissionMode = cfg.Permissions.Mode
	cfg.Permissions.Allow = cleanStringSlice(cfg.Permissions.Allow)
	cfg.Permissions.AllowBash = cleanStringSlice(cfg.Permissions.AllowBash)

	cfg.Skills.Paths = defaultSkillPaths(cfg.WorkDir, cleanAndExpandPaths(cfg.Skills.Paths))
	cfg.Skills.Enabled = cleanStringSlice(cfg.Skills.Enabled)
	cfg.Skills.Disabled = cleanStringSlice(cfg.Skills.Disabled)

	if len(cfg.MCPServers) > 0 {
		cfg.MCP.Servers = cloneMCPServers(cfg.MCPServers)
	}
	cfg.MCP.Servers = defaultMCPServers(cfg.WorkDir, normalizeMCPServers(cfg.MCP.Servers))
	cfg.MCPServers = cloneMCPServers(cfg.MCP.Servers)

	cfg.normalizeSessionMemory()

	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Type == "" {
			p.Type = p.Name
		}
		if p.APIKeyEnv != "" {
			if v := os.Getenv(p.APIKeyEnv); v != "" {
				p.APIKey = v
			}
		}
		if p.BaseURLEnv != "" {
			if v := os.Getenv(p.BaseURLEnv); v != "" {
				p.BaseURL = v
			}
		}
		for j := range p.Models {
			if p.Models[j].Provider == "" {
				p.Models[j].Provider = p.Name
			}
		}
	}

	if len(cfg.Providers) == 0 {
		applyEnvLegacy(cfg)
		return nil
	}

	if err := cfg.validateProviders(); err != nil {
		return err
	}

	prov, mdl, err := cfg.resolveActive()
	if err != nil {
		return err
	}

	if !isSupportedProvider(prov.Type) {
		return fmt.Errorf("provider %q has unsupported type %q (currently supported: anthropic)", prov.Name, prov.Type)
	}

	cfg.APIKey = prov.APIKey
	cfg.BaseURL = prov.BaseURL
	cfg.Model = mdl.ID
	if mdl.MaxContextTokens > 0 {
		cfg.MaxContextTokens = mdl.MaxContextTokens
	} else if cfg.MaxContextTokens <= 0 {
		cfg.MaxContextTokens = 200_000
	}
	if mdl.MaxTokens != nil {
		cfg.MaxTokens = *mdl.MaxTokens
	}
	if mdl.MaxTurns != nil {
		cfg.MaxTurns = *mdl.MaxTurns
	}
	if mdl.Thinking != nil {
		cfg.Thinking = *mdl.Thinking
	}
	if mdl.ThinkingText != nil {
		cfg.ThinkingText = *mdl.ThinkingText
	}
	if mdl.Effort != "" {
		cfg.Effort = mdl.Effort
	}
	if strings.TrimSpace(cfg.Effort) == "" {
		cfg.Effort = "high"
	}
	if cfg.MaxContextTokens <= 0 {
		cfg.MaxContextTokens = 20_000
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 64_000
	}
	if cfg.MaxTurns < 0 {
		cfg.MaxTurns = 0
	}
	return nil
}

type ModelInfo struct {
	Provider    string
	Name        string
	ID          string
	DisplayName string
	Default     bool
	Current     bool
}

func (cfg Config) ListModels() []ModelInfo {
	if len(cfg.Providers) == 0 {
		return []ModelInfo{{Name: cfg.Model, ID: cfg.Model, Current: true}}
	}
	active := cfg.Model
	models := make([]ModelInfo, 0)
	for _, provider := range cfg.Providers {
		for _, model := range provider.Models {
			id := model.ID
			models = append(models, ModelInfo{
				Provider:    provider.Name,
				Name:        model.Name,
				ID:          id,
				DisplayName: model.DisplayName,
				Default:     model.Default,
				Current:     model.Name == active || model.ID == active,
			})
		}
	}
	return models
}

func (cfg Config) WithModel(model string) (Config, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return cfg, fmt.Errorf("model is required")
	}
	next := cfg
	next.Model = model
	if len(next.Providers) > 0 {
		next.Provider = ""
	}
	if err := next.Normalize(); err != nil {
		return cfg, err
	}
	return next, nil
}

func (cfg Config) WithProvider(provider string) (Config, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return cfg, fmt.Errorf("provider is required")
	}
	next := cfg
	next.Provider = provider
	if err := next.Normalize(); err != nil {
		return cfg, err
	}
	return next, nil
}

func (cfg Config) resolveActive() (ResolvedProvider, ModelConfig, error) {
	var prov ResolvedProvider
	if cfg.Provider != "" {
		found := false
		for _, p := range cfg.Providers {
			if p.Name == cfg.Provider {
				prov = ResolvedProvider{Name: p.Name, Type: p.Type, APIKey: p.APIKey, BaseURL: p.BaseURL}
				found = true
				break
			}
		}
		if !found {
			return ResolvedProvider{}, ModelConfig{}, fmt.Errorf("provider %q not found in providers list", cfg.Provider)
		}
	} else {
		candidates := collectProvidersForModel(cfg.Providers, cfg.Model)
		if len(candidates) == 0 {
			candidates = collectDefaultProviders(cfg.Providers)
		}
		if len(candidates) == 0 {
			return ResolvedProvider{}, ModelConfig{}, fmt.Errorf("no provider selected and no default model configured")
		}
		if len(candidates) > 1 {
			names := make([]string, len(candidates))
			for i, c := range candidates {
				names[i] = c.Name
			}
			return ResolvedProvider{}, ModelConfig{}, fmt.Errorf("multiple providers (%s) match — set \"provider\" explicitly", strings.Join(names, ", "))
		}
		prov = candidates[0]
	}

	if cfg.Model == "" {
		for _, m := range cfg.modelsOf(prov.Name) {
			if m.Default {
				return prov, m, nil
			}
		}
		return prov, ModelConfig{}, fmt.Errorf("no default model configured in provider %q", prov.Name)
	}

	for _, m := range cfg.modelsOf(prov.Name) {
		if m.Name == cfg.Model || m.ID == cfg.Model {
			return prov, m, nil
		}
	}
	return prov, ModelConfig{}, fmt.Errorf("model %q not found in provider %q", cfg.Model, prov.Name)
}

func collectProvidersForModel(providers []ProviderConfig, model string) []ResolvedProvider {
	if model == "" {
		return nil
	}
	var out []ResolvedProvider
	for _, p := range providers {
		for _, m := range p.Models {
			if m.Name == model || m.ID == model {
				out = append(out, ResolvedProvider{Name: p.Name, Type: p.Type, APIKey: p.APIKey, BaseURL: p.BaseURL})
				break
			}
		}
	}
	return out
}

func collectDefaultProviders(providers []ProviderConfig) []ResolvedProvider {
	var out []ResolvedProvider
	for _, p := range providers {
		for _, m := range p.Models {
			if m.Default {
				out = append(out, ResolvedProvider{Name: p.Name, Type: p.Type, APIKey: p.APIKey, BaseURL: p.BaseURL})
				break
			}
		}
	}
	return out
}

func (cfg Config) modelsOf(name string) []ModelConfig {
	for _, p := range cfg.Providers {
		if p.Name == name {
			return p.Models
		}
	}
	return nil
}

func (cfg Config) validateProviders() error {
	defaultCount := 0
	for _, p := range cfg.Providers {
		if p.Name == "" {
			return fmt.Errorf("provider name is required")
		}
		for _, m := range p.Models {
			if m.Name == "" {
				return fmt.Errorf("model name is required (provider %q)", p.Name)
			}
			if m.ID == "" {
				return fmt.Errorf("model id is required (provider %q, model %q)", p.Name, m.Name)
			}
			if m.Default {
				defaultCount++
			}
		}
	}
	if defaultCount > 1 {
		return fmt.Errorf("multiple models have default: true — exactly one model may be the default")
	}
	return nil
}

func isSupportedProvider(providerType string) bool {
	return providerType == "anthropic"
}

func applyEnvLegacy(cfg *Config) {
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if cfg.Model == "" {
		cfg.Model = anthropic.DefaultModel
	}
	if cfg.MaxContextTokens <= 0 {
		cfg.MaxContextTokens = 200_000
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 64_000
	}
	if cfg.MaxTurns < 0 {
		cfg.MaxTurns = 0
	}
	if strings.TrimSpace(cfg.Effort) == "" {
		cfg.Effort = "high"
	}
}

func (cfg *Config) normalizeSessionMemory() {
	if cfg.Session.DefaultSession == "" {
		cfg.Session.DefaultSession = "main"
	}
	if cfg.Session.Dir == "" {
		cfg.Session.Dir = DefaultSessionDir(cfg.WorkDir)
	} else {
		cfg.Session.Dir = expandPath(cfg.Session.Dir)
		if !filepath.IsAbs(cfg.Session.Dir) && cfg.WorkDir != "" {
			cfg.Session.Dir = filepath.Join(cfg.WorkDir, cfg.Session.Dir)
		}
	}
	if cfg.Memory.MaxRecentTurns <= 0 {
		cfg.Memory.MaxRecentTurns = 20
	}
	if cfg.Memory.Dir == "" {
		cfg.Memory.Dir = DefaultMemoryDir(cfg.WorkDir)
	} else {
		cfg.Memory.Dir = expandPath(cfg.Memory.Dir)
		if !filepath.IsAbs(cfg.Memory.Dir) && cfg.WorkDir != "" {
			cfg.Memory.Dir = filepath.Join(cfg.WorkDir, cfg.Memory.Dir)
		}
	}
	if cfg.Memory.SummaryThresholdTokens <= 0 {
		cfg.Memory.SummaryThresholdTokens = 60_000
	}
	if cfg.Memory.CompactionTriggerPercent <= 0 {
		cfg.Memory.CompactionTriggerPercent = 85
	}
	if cfg.Memory.CompactionTargetPercent <= 0 {
		cfg.Memory.CompactionTargetPercent = 50
	}
	if cfg.Memory.CompactionTriggerPercent > 100 {
		cfg.Memory.CompactionTriggerPercent = 100
	}
	if cfg.Memory.CompactionTargetPercent > 100 {
		cfg.Memory.CompactionTargetPercent = 100
	}
	if cfg.Memory.CompactionTargetPercent >= cfg.Memory.CompactionTriggerPercent {
		cfg.Memory.CompactionTargetPercent = cfg.Memory.CompactionTriggerPercent - 10
		if cfg.Memory.CompactionTargetPercent < 1 {
			cfg.Memory.CompactionTargetPercent = 1
		}
	}
	if cfg.Memory.RetrievalLimit <= 0 {
		cfg.Memory.RetrievalLimit = 8
	}
	if cfg.Memory.RetrievalM2Limit <= 0 {
		cfg.Memory.RetrievalM2Limit = 4
	}
	if cfg.Memory.RetrievalM3Limit <= 0 {
		cfg.Memory.RetrievalM3Limit = 3
	}
	if cfg.Memory.RetrievalM4Limit <= 0 {
		cfg.Memory.RetrievalM4Limit = 3
	}
	if cfg.Memory.RetrievalM5Limit <= 0 {
		cfg.Memory.RetrievalM5Limit = 2
	}
	if cfg.Memory.TierM1TTLHours <= 0 {
		cfg.Memory.TierM1TTLHours = 12
	}
	if cfg.Memory.TierM2TTLHours <= 0 {
		cfg.Memory.TierM2TTLHours = 72
	}
	if cfg.Memory.PromotionAccessThreshold <= 0 {
		cfg.Memory.PromotionAccessThreshold = 3
	}
	if cfg.Memory.PromotionConfidence <= 0 {
		cfg.Memory.PromotionConfidence = 0.75
	}
}

func PersistencePath(explicitConfigPath, workDir string) string {
	if strings.TrimSpace(explicitConfigPath) != "" {
		return filepath.Clean(expandPath(explicitConfigPath))
	}
	return DefaultRuntimeSettingsPath()
}

func SaveLocalOverrides(path string, updates map[string]any) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("settings path is required")
	}
	if len(updates) == 0 {
		return nil
	}

	path = filepath.Clean(expandPath(path))
	data := map[string]any{}
	if existing, err := os.ReadFile(path); err == nil {
		if len(strings.TrimSpace(string(existing))) > 0 {
			if err := json.Unmarshal(existing, &data); err != nil {
				return fmt.Errorf("parse existing settings %q: %w", path, err)
			}
			if data == nil {
				return fmt.Errorf("existing settings %q must be a JSON object", path)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing settings %q: %w", path, err)
	}

	for key, value := range updates {
		if key == "" {
			continue
		}
		if existingObject, ok := data[key].(map[string]any); ok {
			if updateObject, ok := value.(map[string]any); ok {
				merged := make(map[string]any, len(existingObject)+len(updateObject))
				for k, v := range existingObject {
					merged[k] = v
				}
				for k, v := range updateObject {
					merged[k] = v
				}
				data[key] = merged
				continue
			}
		}
		data[key] = value
	}

	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings %q: %w", path, err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create settings directory %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return fmt.Errorf("write settings %q: %w", path, err)
	}
	return nil
}

func discoverDefaultPaths() []string {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(home, configDirName, settingsFileName),
		filepath.Join(home, configDirName, settingsLocalName),
		filepath.Join(cwd, configDirName, settingsFileName),
		filepath.Join(cwd, configDirName, settingsLocalName),
	}
	return uniqueNonEmpty(paths)
}

func loadOptionalFile(cfg *Config, path string) error {
	if path == "" {
		return nil
	}
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat config %q: %w", path, err)
	}
	return loadFile(cfg, path)
}

func loadFile(cfg *Config, path string) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	if err := applyJSONConfig(cfg, data); err != nil {
		return fmt.Errorf("parse config %q: %w", path, err)
	}
	return nil
}

func applyJSONConfig(cfg *Config, data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key, value := range raw {
		switch key {
		case "api_key":
			if err := json.Unmarshal(value, &cfg.APIKey); err != nil {
				return err
			}
		case "base_url":
			if err := json.Unmarshal(value, &cfg.BaseURL); err != nil {
				return err
			}
		case "model":
			if err := json.Unmarshal(value, &cfg.Model); err != nil {
				return err
			}
		case "max_tokens":
			if err := json.Unmarshal(value, &cfg.MaxTokens); err != nil {
				return err
			}
		case "system_prompt":
			if err := json.Unmarshal(value, &cfg.SystemPrompt); err != nil {
				return err
			}
		case "work_dir":
			if err := json.Unmarshal(value, &cfg.WorkDir); err != nil {
				return err
			}
		case "max_turns":
			if err := json.Unmarshal(value, &cfg.MaxTurns); err != nil {
				return err
			}
		case "stream":
			if err := json.Unmarshal(value, &cfg.Stream); err != nil {
				return err
			}
		case "thinking":
			if err := json.Unmarshal(value, &cfg.Thinking); err != nil {
				return err
			}
		case "thinking_text":
			if err := json.Unmarshal(value, &cfg.ThinkingText); err != nil {
				return err
			}
		case "effort":
			if err := json.Unmarshal(value, &cfg.Effort); err != nil {
				return err
			}
		case "permission_mode":
			if err := json.Unmarshal(value, &cfg.PermissionMode); err != nil {
				return err
			}
		case "permissions":
			if err := applyPermissionsJSON(&cfg.Permissions, value); err != nil {
				return err
			}
		case "hooks":
			if err := applyHooksJSON(&cfg.Hooks, value); err != nil {
				return err
			}
		case "skills":
			if err := applySkillsJSON(&cfg.Skills, value); err != nil {
				return err
			}
		case "mcp":
			if err := applyMCPJSON(&cfg.MCP, value); err != nil {
				return err
			}
		case "mcp_servers", "mcpServers":
			servers, err := parseMCPServers(value)
			if err != nil {
				return err
			}
			cfg.MCPServers = servers
		case "session":
			if err := json.Unmarshal(value, &cfg.Session); err != nil {
				return err
			}
		case "memory":
			if err := json.Unmarshal(value, &cfg.Memory); err != nil {
				return err
			}
		case "provider":
			if err := json.Unmarshal(value, &cfg.Provider); err != nil {
				return err
			}
		case "providers":
			if err := json.Unmarshal(value, &cfg.Providers); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyPermissionsJSON(cfg *permission.Config, data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key, value := range raw {
		switch key {
		case "mode":
			if err := json.Unmarshal(value, &cfg.Mode); err != nil {
				return err
			}
		case "allow", "allowed_tools":
			if err := json.Unmarshal(value, &cfg.Allow); err != nil {
				return err
			}
		case "allow_bash", "allowed_bash":
			if err := json.Unmarshal(value, &cfg.AllowBash); err != nil {
				return err
			}
		}
	}
	return nil
}

func applySkillsJSON(cfg *SkillsConfig, data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key, value := range raw {
		switch key {
		case "paths":
			if err := json.Unmarshal(value, &cfg.Paths); err != nil {
				return err
			}
		case "enabled":
			if err := json.Unmarshal(value, &cfg.Enabled); err != nil {
				return err
			}
		case "disabled":
			if err := json.Unmarshal(value, &cfg.Disabled); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyMCPJSON(cfg *MCPConfig, data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key, value := range raw {
		switch key {
		case "servers", "mcp_servers", "mcpServers":
			servers, err := parseMCPServers(value)
			if err != nil {
				return err
			}
			cfg.Servers = servers
		}
	}
	return nil
}

func applyHooksJSON(cfg *hook.Config, data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if events, ok := raw["events"]; ok {
		return json.Unmarshal(events, &cfg.Events)
	}
	if cfg.Events == nil {
		cfg.Events = make(map[hook.EventName][]hook.MatcherConfig)
	}
	for key, value := range raw {
		var groups []hook.MatcherConfig
		if err := json.Unmarshal(value, &groups); err != nil {
			return err
		}
		cfg.Events[hook.EventName(key)] = groups
	}
	return nil
}

func parseMCPServers(data []byte) ([]MCPServerConfig, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var servers []MCPServerConfig
		if err := json.Unmarshal(data, &servers); err != nil {
			return nil, err
		}
		return normalizeMCPServers(servers), nil
	}

	var named map[string]json.RawMessage
	if err := json.Unmarshal(data, &named); err != nil {
		return nil, err
	}
	servers := make([]MCPServerConfig, 0, len(named))
	for name, value := range named {
		var server MCPServerConfig
		if err := json.Unmarshal(value, &server); err != nil {
			return nil, err
		}
		if server.Name == "" {
			server.Name = name
		}
		servers = append(servers, server)
	}
	return normalizeMCPServers(servers), nil
}

func normalizeMCPServers(servers []MCPServerConfig) []MCPServerConfig {
	if len(servers) == 0 {
		return nil
	}
	out := make([]MCPServerConfig, 0, len(servers))
	for _, server := range servers {
		if server.Name == "" {
			continue
		}
		server.Transport = strings.TrimSpace(server.Transport)
		if server.Transport == "" {
			server.Transport = strings.TrimSpace(server.Type)
		}
		if server.Transport == "" {
			server.Transport = "stdio"
		}
		server.Command = expandPath(server.Command)
		server.URL = strings.TrimSpace(server.URL)
		server.Args = cleanStringSlice(server.Args)
		server.Headers = cleanStringMap(server.Headers)
		server.Env = cleanStringMap(server.Env)
		out = append(out, server)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func defaultSkillPaths(workDir string, configured []string) []string {
	return uniqueNonEmpty(append(DefaultSkillDirs(workDir), configured...))
}

func defaultMCPServers(workDir string, configured []MCPServerConfig) []MCPServerConfig {
	merged := map[string]MCPServerConfig{}
	for _, server := range configured {
		merged[server.Name] = server
	}
	for _, path := range defaultMCPConfigPaths(workDir) {
		servers, err := loadMCPServersFromFile(path)
		if err != nil {
			continue
		}
		for _, server := range servers {
			if _, exists := merged[server.Name]; exists {
				continue
			}
			merged[server.Name] = server
		}
	}
	if len(merged) == 0 {
		return nil
	}
	out := make([]MCPServerConfig, 0, len(merged))
	for _, server := range merged {
		out = append(out, server)
	}
	return normalizeMCPServers(out)
}

func defaultMCPConfigPaths(workDir string) []string {
	paths := []string{
		filepath.Join(UserConfigDir(), settingsFileName),
		filepath.Join(UserConfigDir(), settingsLocalName),
	}
	if projectDir := ProjectConfigDir(workDir); projectDir != "" {
		paths = append(paths,
			filepath.Join(projectDir, settingsFileName),
			filepath.Join(projectDir, settingsLocalName),
		)
	}
	return uniqueNonEmpty(paths)
}

func loadMCPServersFromFile(path string) ([]MCPServerConfig, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if value, ok := raw["mcp"]; ok {
		var cfg MCPConfig
		if err := applyMCPJSON(&cfg, value); err != nil {
			return nil, err
		}
		if len(cfg.Servers) > 0 {
			return normalizeMCPServers(cfg.Servers), nil
		}
	}
	if value, ok := raw["mcp_servers"]; ok {
		return parseMCPServers(value)
	}
	if value, ok := raw["mcpServers"]; ok {
		return parseMCPServers(value)
	}
	return nil, nil
}

func cloneMCPServers(servers []MCPServerConfig) []MCPServerConfig {
	if len(servers) == 0 {
		return nil
	}
	out := make([]MCPServerConfig, len(servers))
	copy(out, servers)
	for i := range out {
		out[i].Args = append([]string(nil), out[i].Args...)
		out[i].Headers = cleanStringMap(out[i].Headers)
		out[i].Env = cleanStringMap(out[i].Env)
	}
	return out
}

func cleanAndExpandPaths(paths []string) []string {
	paths = cleanStringSlice(paths)
	for i := range paths {
		paths[i] = expandPath(paths[i])
	}
	return paths
}

func expandPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			value = filepath.Join(home, strings.TrimPrefix(value, "~"))
		}
	}
	return os.ExpandEnv(value)
}

func cleanStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func cleanStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
