package unit_tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/codeplus-agent/internal/anthropic"
	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/permission"
)

func TestLoadEmptyConfigDefaults(t *testing.T) {
	cfg := config.Default()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() = %v", err)
	}
	if cfg.Model != anthropic.DefaultModel {
		t.Fatalf("expected default model %s, got %q", anthropic.DefaultModel, cfg.Model)
	}
	if cfg.MaxContextTokens != 200_000 {
		t.Fatalf("expected default MaxContextTokens 200000, got %d", cfg.MaxContextTokens)
	}
	if cfg.MaxTokens != 64_000 {
		t.Fatalf("expected default MaxTokens 64000, got %d", cfg.MaxTokens)
	}
	if cfg.MaxTurns != 0 {
		t.Fatalf("expected default MaxTurns 0 (unlimited), got %d", cfg.MaxTurns)
	}
	if !cfg.Thinking {
		t.Fatalf("expected Thinking = true by default")
	}
	if cfg.Effort != "high" {
		t.Fatalf("expected default effort high, got %q", cfg.Effort)
	}
	if cfg.PermissionMode != permission.ModeAuto {
		t.Fatalf("expected default permission mode auto, got %q", cfg.PermissionMode)
	}
}

func TestLoadCreatesDefaultSettingsOnFirstStart(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(t.TempDir(), "project")
	mustMkdirAll(t, home)
	mustMkdirAll(t, project)
	oldHome := os.Getenv("HOME")
	oldUserProfile := os.Getenv("USERPROFILE")
	oldPwd, _ := os.Getwd()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(oldPwd)
		_ = os.Setenv("HOME", oldHome)
		_ = os.Setenv("USERPROFILE", oldUserProfile)
	}()

	settingsPath := filepath.Join(config.UserConfigDir(), "settings.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("expected no settings.json before first load, got err=%v", err)
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("expected initialized settings.json, got err=%v", err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	text := string(data)
	for _, want := range []string{"\"providers\"", anthropic.DefaultModel, "\"max_context_tokens\": 200000", "\"max_tokens\": 64000", "\"max_turns\": 0", "\"effort\": \"high\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in initialized settings.json: %s", want, text)
		}
	}
	if cfg.Model != anthropic.DefaultModel {
		t.Fatalf("expected initialized config model %s, got %q", anthropic.DefaultModel, cfg.Model)
	}
}

func TestProviderModelMissingLimitsUseDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"provider": "anthropic",
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"api_key": "sk-test",
				"models": [
					{"name":"default","id":"`+anthropic.DefaultModel+`","default":true}
				]
			}
		]
	}`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.MaxContextTokens != 200_000 {
		t.Fatalf("expected default MaxContextTokens 200000, got %d", cfg.MaxContextTokens)
	}
	if cfg.MaxTokens != 64_000 {
		t.Fatalf("expected default MaxTokens 64000, got %d", cfg.MaxTokens)
	}
	if cfg.MaxTurns != 0 {
		t.Fatalf("expected default MaxTurns 0, got %d", cfg.MaxTurns)
	}
	if cfg.Effort != "high" {
		t.Fatalf("expected default effort high, got %q", cfg.Effort)
	}
}

func TestLoadLegacyFlatJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"api_key": "sk-my-legacy-key",
		"model": "claude-sonnet-4-6",
		"max_tokens": 32000,
		"thinking": false,
		"effort": "medium",
		"max_turns": 5
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.APIKey != "sk-my-legacy-key" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
	if cfg.Model != "claude-sonnet-4-6" {
		t.Fatalf("Model = %q", cfg.Model)
	}
	if cfg.MaxTokens != 32_000 {
		t.Fatalf("MaxTokens = %d", cfg.MaxTokens)
	}
	if cfg.Thinking {
		t.Fatalf("Thinking should be false")
	}
	if cfg.Effort != "medium" {
		t.Fatalf("Effort = %q", cfg.Effort)
	}
	if cfg.MaxTurns != 5 {
		t.Fatalf("MaxTurns = %d", cfg.MaxTurns)
	}
	wantSkillDirs := []string{
		filepath.Join(config.UserConfigDir(), "skills"),
		filepath.Join(config.ProjectConfigDir(cfg.WorkDir), "skills"),
		filepath.Join(config.UserConfigDir(), "my-skill"),
		filepath.Join(config.ProjectConfigDir(cfg.WorkDir), "my-skill"),
	}
	if len(cfg.Skills.Paths) != len(wantSkillDirs) {
		t.Fatalf("Skills.Paths = %#v", cfg.Skills.Paths)
	}
	for i, want := range wantSkillDirs {
		if cfg.Skills.Paths[i] != want {
			t.Fatalf("Skills.Paths[%d] = %q, want %q", i, cfg.Skills.Paths[i], want)
		}
	}
}

func TestLoadAgentcodeLayeredConfig(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	project := filepath.Join(dir, "project")
	globalDir := filepath.Join(home, ".agentcode")
	projectDir := filepath.Join(project, ".agentcode")
	mustMkdirAll(t, globalDir)
	mustMkdirAll(t, projectDir)

	writeFile(t, filepath.Join(globalDir, "settings.json"), `{
		"permission_mode": "auto",
		"permissions": {
			"allow": ["Read", "Glob"],
			"allow_bash": ["git status"]
		},
		"skills": {
			"paths": ["~/.agentcode/skills", " ./skills "],
			"enabled": ["verify"],
			"disabled": ["debug"]
		},
		"mcp": {
			"servers": {
				"filesystem": {
					"command": "npx",
					"args": ["-y", "@modelcontextprotocol/server-filesystem", "."]
				}
			}
		},
		"hooks": {
			"PreToolUse": [
				{
					"matcher": "Bash",
					"hooks": [
						{"type": "command", "command": "rtk hook claude"}
					]
				}
			]
		}
	}`)

	writeFile(t, filepath.Join(projectDir, "settings.local.json"), `{
		"permissions": {
			"allow": ["Read", "View", "Read"]
		},
		"mcp_servers": {
			"memory": {
				"transport": "sse",
				"url": "https://mcp.example.com/sse"
			}
		}
	}`)

	oldHome := os.Getenv("HOME")
	oldUserProfile := os.Getenv("USERPROFILE")
	oldPwd, _ := os.Getwd()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(oldPwd)
		_ = os.Setenv("HOME", oldHome)
		_ = os.Setenv("USERPROFILE", oldUserProfile)
	}()

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load layered config = %v", err)
	}
	if cfg.PermissionMode != permission.ModeAuto {
		t.Fatalf("PermissionMode = %q", cfg.PermissionMode)
	}
	if len(cfg.Permissions.Allow) != 2 || cfg.Permissions.Allow[0] != "Read" || cfg.Permissions.Allow[1] != "View" {
		t.Fatalf("Permissions.Allow = %#v", cfg.Permissions.Allow)
	}
	if len(cfg.Permissions.AllowBash) != 1 || cfg.Permissions.AllowBash[0] != "git status" {
		t.Fatalf("Permissions.AllowBash = %#v", cfg.Permissions.AllowBash)
	}
	wantPaths := []string{
		filepath.Join(config.UserConfigDir(), "skills"),
		filepath.Join(config.ProjectConfigDir(cfg.WorkDir), "skills"),
		filepath.Join(config.UserConfigDir(), "my-skill"),
		filepath.Join(config.ProjectConfigDir(cfg.WorkDir), "my-skill"),
		"./skills",
	}
	if len(cfg.Skills.Paths) != len(wantPaths) {
		t.Fatalf("Skills.Paths = %#v", cfg.Skills.Paths)
	}
	for i, want := range wantPaths {
		if cfg.Skills.Paths[i] != want {
			t.Fatalf("Skills.Paths[%d] = %q, want %q", i, cfg.Skills.Paths[i], want)
		}
	}
	if len(cfg.Skills.Enabled) != 1 || cfg.Skills.Enabled[0] != "verify" {
		t.Fatalf("Skills.Enabled = %#v", cfg.Skills.Enabled)
	}
	if len(cfg.Skills.Disabled) != 1 || cfg.Skills.Disabled[0] != "debug" {
		t.Fatalf("Skills.Disabled = %#v", cfg.Skills.Disabled)
	}
	if len(cfg.MCP.Servers) != 2 || cfg.MCP.Servers[0].Name != "filesystem" || cfg.MCP.Servers[1].Name != "memory" {
		t.Fatalf("MCP.Servers = %#v", cfg.MCP.Servers)
	}
	if len(cfg.Hooks.Events) != 1 || len(cfg.Hooks.Events["PreToolUse"]) != 1 {
		t.Fatalf("Hooks.Events = %#v", cfg.Hooks.Events)
	}
}

func TestLoadExplicitPermissionsConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"permission_mode": "yolo",
		"permissions": {
			"mode": "auto",
			"allow": ["Read", "Glob"],
			"allow_bash": ["git status", "go test"]
		}
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.PermissionMode != permission.ModeAuto {
		t.Fatalf("expected permissions.mode to win, got %q", cfg.PermissionMode)
	}
	if len(cfg.Permissions.Allow) != 2 {
		t.Fatalf("Permissions.Allow = %#v", cfg.Permissions.Allow)
	}
	if len(cfg.Permissions.AllowBash) != 2 {
		t.Fatalf("Permissions.AllowBash = %#v", cfg.Permissions.AllowBash)
	}
}

func TestLoadMCPTransportConfigs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"mcp_servers": {
			"remote-sse": {
				"transport": "sse",
				"url": "https://example.com/sse",
				"headers": {"Authorization": "Bearer token"}
			},
			"remote-http": {
				"transport": "http",
				"url": "https://example.com/mcp"
			}
		}
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if len(cfg.MCP.Servers) != 2 {
		t.Fatalf("MCP.Servers = %#v", cfg.MCP.Servers)
	}
}

func TestMultiModelSelectsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"api_key": "sk-test",
				"models": [
					{"name":"sonnet","id":"claude-sonnet-4-6","default":false},
					{"name":"opus","id":"claude-opus-4-8","default":true,"max_tokens":64000,"thinking":true,"effort":"high"},
					{"name":"haiku","id":"claude-haiku-4-5"}
				]
			}
		]
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.Model != "claude-opus-4-8" {
		t.Fatalf("expected default model claude-opus-4-8, got %q", cfg.Model)
	}
	if cfg.MaxTokens != 64_000 {
		t.Fatalf("MaxTokens = %d", cfg.MaxTokens)
	}
	if !cfg.Thinking {
		t.Fatalf("Thinking should be true")
	}
	if cfg.Effort != "high" {
		t.Fatalf("Effort = %q", cfg.Effort)
	}
	if cfg.APIKey != "sk-test" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
}

func TestMultiModelSelectsExplicitModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"model": "sonnet",
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"models": [
					{"name":"sonnet","id":"claude-sonnet-4-6","max_tokens":16000},
					{"name":"opus","id":"claude-opus-4-8","default":true}
				]
			}
		]
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.Model != "claude-sonnet-4-6" {
		t.Fatalf("expected sonnet model, got %q", cfg.Model)
	}
	if cfg.MaxTokens != 16_000 {
		t.Fatalf("MaxTokens = %d", cfg.MaxTokens)
	}
}

func TestFastModelDefaultsFromFastModelConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"model": "sonnet",
		"providers": [{
			"name": "anthropic",
			"type": "anthropic",
			"models": [
				{"name":"sonnet","id":"claude-sonnet","default":true},
				{"name":"haiku","id":"claude-haiku","fast":true}
			]
		}]
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.FastModel != "claude-haiku" {
		t.Fatalf("expected fast model claude-haiku, got %q", cfg.FastModel)
	}
}

func TestSessionAndMemoryDirsDefaultUnderUserDir(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Session.Dir = ""
	cfg.Memory.Dir = ""
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() = %v", err)
	}
	if cfg.Session.Dir != config.DefaultSessionDir(cfg.WorkDir) {
		t.Fatalf("expected session dir %q, got %q", config.DefaultSessionDir(cfg.WorkDir), cfg.Session.Dir)
	}
	if cfg.Memory.Dir != config.DefaultMemoryDir(cfg.WorkDir) {
		t.Fatalf("expected memory dir %q, got %q", config.DefaultMemoryDir(cfg.WorkDir), cfg.Memory.Dir)
	}
}

func TestMemoryConfigCompactionDefaults(t *testing.T) {
	cfg := config.Default()
	if cfg.Memory.CompactionTriggerPercent != 85 {
		t.Fatalf("expected default trigger 85, got %d", cfg.Memory.CompactionTriggerPercent)
	}
	if cfg.Memory.SummaryTriggerPercent != 50 {
		t.Fatalf("expected default summary trigger 50, got %d", cfg.Memory.SummaryTriggerPercent)
	}
	if cfg.Memory.CompactionTargetPercent != 15 {
		t.Fatalf("expected default target 15, got %d", cfg.Memory.CompactionTargetPercent)
	}
	if cfg.Memory.TierM1TTLHours != 12 {
		t.Fatalf("expected default M1 TTL 12, got %d", cfg.Memory.TierM1TTLHours)
	}
	if cfg.Memory.TierM2TTLHours != 72 {
		t.Fatalf("expected default M2 TTL 72, got %d", cfg.Memory.TierM2TTLHours)
	}
	if cfg.Memory.PromotionAccessThreshold != 3 {
		t.Fatalf("expected default promotion access threshold 3, got %d", cfg.Memory.PromotionAccessThreshold)
	}
	if cfg.Memory.PromotionConfidence != 0.75 {
		t.Fatalf("expected default promotion confidence 0.75, got %v", cfg.Memory.PromotionConfidence)
	}
	if cfg.Memory.RetrievalContextPercent != 10 || cfg.Memory.RetrievalMinTokens != 10_000 || cfg.Memory.RetrievalMaxTokens != 50_000 {
		t.Fatalf("unexpected retrieval token defaults: %#v", cfg.Memory)
	}
	if cfg.Memory.RetrievalM2Limit != 4 || cfg.Memory.RetrievalM3Limit != 3 || cfg.Memory.RetrievalM4Limit != 3 || cfg.Memory.RetrievalM5Limit != 2 {
		t.Fatalf("unexpected retrieval tier defaults: %#v", cfg.Memory)
	}
	cfg.Memory.CompactionTargetPercent = 95
	cfg.Memory.CompactionTriggerPercent = 85
	cfg.Memory.SummaryTriggerPercent = 0
	cfg.Memory.TierM1TTLHours = 0
	cfg.Memory.TierM2TTLHours = 0
	cfg.Memory.PromotionAccessThreshold = 0
	cfg.Memory.PromotionConfidence = 0
	cfg.Memory.RetrievalContextPercent = 0
	cfg.Memory.RetrievalMinTokens = 0
	cfg.Memory.RetrievalMaxTokens = 0
	cfg.Memory.RetrievalM2Limit = 0
	cfg.Memory.RetrievalM3Limit = 0
	cfg.Memory.RetrievalM4Limit = 0
	cfg.Memory.RetrievalM5Limit = 0
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() = %v", err)
	}
	if cfg.Memory.CompactionTargetPercent >= cfg.Memory.CompactionTriggerPercent {
		t.Fatalf("expected target < trigger, got target=%d trigger=%d", cfg.Memory.CompactionTargetPercent, cfg.Memory.CompactionTriggerPercent)
	}
	if cfg.Memory.SummaryTriggerPercent != 50 || cfg.Memory.TierM1TTLHours != 12 || cfg.Memory.TierM2TTLHours != 72 || cfg.Memory.PromotionAccessThreshold != 3 || cfg.Memory.PromotionConfidence != 0.75 {
		t.Fatalf("memory normalize defaults not restored: %#v", cfg.Memory)
	}
	if cfg.Memory.RetrievalContextPercent != 10 || cfg.Memory.RetrievalMinTokens != 10_000 || cfg.Memory.RetrievalMaxTokens != 50_000 {
		t.Fatalf("retrieval token defaults not restored: %#v", cfg.Memory)
	}
	if cfg.Memory.RetrievalM2Limit != 4 || cfg.Memory.RetrievalM3Limit != 3 || cfg.Memory.RetrievalM4Limit != 3 || cfg.Memory.RetrievalM5Limit != 2 {
		t.Fatalf("retrieval tier defaults not restored: %#v", cfg.Memory)
	}
}

func TestSessionAndMemoryRelativeDirsUseWorkDir(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Session.Dir = "session-data"
	cfg.Memory.Dir = "memory-data"
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() = %v", err)
	}
	wantSession := filepath.Join(cfg.WorkDir, "session-data")
	if cfg.Session.Dir != wantSession {
		t.Fatalf("expected session dir %q, got %q", wantSession, cfg.Session.Dir)
	}
	wantMemory := filepath.Join(cfg.WorkDir, "memory-data")
	if cfg.Memory.Dir != wantMemory {
		t.Fatalf("expected memory dir %q, got %q", wantMemory, cfg.Memory.Dir)
	}
}

func TestMultiModelSelectsExplicitProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"provider": "anthropic-alt",
		"model": "opus",
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"api_key": "sk-primary",
				"models": [{"name":"sonnet","id":"claude-sonnet-4-6"}]
			},
			{
				"name": "anthropic-alt",
				"type": "anthropic",
				"api_key": "sk-alt",
				"models": [{"name":"opus","id":"claude-opus-4-8"}]
			}
		]
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.Model != "claude-opus-4-8" {
		t.Fatalf("expected claude-opus-4-8, got %q", cfg.Model)
	}
	if cfg.APIKey != "sk-alt" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
}

func TestMultiModelUnsupportedProviderTypeError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"provider": "openai",
		"model": "gpt5",
		"providers": [
			{
				"name": "openai",
				"type": "openai",
				"api_key": "sk-openai-test",
				"models": [{"name":"gpt5","id":"gpt-5"}]
			}
		]
	}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected unsupported provider error, got nil")
	}
}

func TestMultiModelMultipleDefaultsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"models": [
					{"name":"sonnet","id":"claude-sonnet-4-6","default":true},
					{"name":"opus","id":"claude-opus-4-8","default":true}
				]
			}
		]
	}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for multiple defaults, got nil")
	}
}

func TestMultiModelMissingProviderError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"provider": "nonexistent",
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"models": [{"name":"opus","id":"claude-opus-4-8"}]
			}
		]
	}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for missing provider, got nil")
	}
}

func TestMultiModelMissingModelError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"model": "nonexistent",
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"models": [{"name":"opus","id":"claude-opus-4-8"}]
			}
		]
	}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for missing model, got nil")
	}
}

func TestMultiModelProviderEnvResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"api_key_env": "TEST_KEY_ENV",
				"base_url_env": "TEST_URL_ENV",
				"models": [{"name":"opus","id":"claude-opus-4-8","default":true}]
			}
		]
	}`)

	t.Setenv("TEST_KEY_ENV", "sk-from-env")
	t.Setenv("TEST_URL_ENV", "https://api.example.com")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.APIKey != "sk-from-env" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
	if cfg.BaseURL != "https://api.example.com" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
}

func TestMultiModelProviderEnvFallsBackToLegacyIfNoProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{}`)

	t.Setenv("ANTHROPIC_API_KEY", "sk-legacy-env")
	t.Setenv("ANTHROPIC_BASE_URL", "https://legacy.example.com")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.APIKey != "sk-legacy-env" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
	if cfg.BaseURL != "https://legacy.example.com" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
}

func TestMultiModelSetsProviderOnModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"model": "haiku",
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"models": [
					{"name":"sonnet","id":"claude-sonnet-4-6"},
					{"name":"haiku","id":"claude-haiku-4-5","max_tokens":4096,"thinking":false}
				]
			}
		]
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	if cfg.Model != "claude-haiku-4-5" {
		t.Fatalf("expected haiku model, got %q", cfg.Model)
	}
	if cfg.MaxTokens != 4_096 {
		t.Fatalf("MaxTokens = %d", cfg.MaxTokens)
	}
	if cfg.Thinking {
		t.Fatalf("Thinking should be false")
	}
}

func TestConfigListModelsAndWithModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{
		"model": "sonnet",
		"max_turns": 10,
		"effort": "low",
		"providers": [
			{
				"name": "anthropic",
				"type": "anthropic",
				"models": [
					{"name":"sonnet","id":"claude-sonnet-4-6","display_name":"Sonnet","max_context_tokens":200000,"max_tokens":16000},
					{"name":"opus","id":"claude-opus-4-8","default":true,"max_context_tokens":1000000,"max_tokens":64000,"max_turns":22,"thinking":true,"effort":"high"}
				]
			}
		]
	}`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(%q) = %v", path, err)
	}
	models := cfg.ListModels()
	if len(models) != 2 {
		t.Fatalf("ListModels len = %d", len(models))
	}
	if !models[0].Current || models[0].DisplayName != "Sonnet" {
		t.Fatalf("first model = %#v", models[0])
	}

	next, err := cfg.WithModel("opus")
	if err != nil {
		t.Fatalf("WithModel(opus) = %v", err)
	}
	if next.Model != "claude-opus-4-8" || next.MaxContextTokens != 1_000_000 || next.MaxTokens != 64_000 || next.MaxTurns != 22 || !next.Thinking || next.Effort != "high" {
		t.Fatalf("next config = %#v", next)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeFile(t, path, `{not json}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestPersistencePath(t *testing.T) {
	workDir := t.TempDir()
	explicit := filepath.Join(workDir, "custom.json")
	if got := config.PersistencePath(explicit, workDir); got != explicit {
		t.Fatalf("expected explicit path %q, got %q", explicit, got)
	}
	want := config.DefaultRuntimeSettingsPath()
	if got := config.PersistencePath("", workDir); got != want {
		t.Fatalf("expected default path %q, got %q", want, got)
	}
}

func TestDefaultSessionDirSanitizesWindowsDrivePath(t *testing.T) {
	got := config.DefaultSessionDir(`C:\work\project-a`)
	wantSuffix := filepath.Join("C__work_project-a", "sessions")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("expected session dir suffix %q, got %q", wantSuffix, got)
	}
}

func TestSaveLocalOverridesCreatesAndMerges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".agentcode", "settings.local.json")
	if err := config.SaveLocalOverrides(path, map[string]any{"model": "opus"}); err != nil {
		t.Fatalf("SaveLocalOverrides create = %v", err)
	}
	if err := config.SaveLocalOverrides(path, map[string]any{
		"session":  map[string]any{"default_session": "feature"},
		"provider": "anthropic-alt",
	}); err != nil {
		t.Fatalf("SaveLocalOverrides merge = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load persisted settings = %v", err)
	}
	if cfg.Model != "opus" {
		t.Fatalf("expected model alias opus preserved, got %q", cfg.Model)
	}
	if cfg.Provider != "anthropic-alt" {
		t.Fatalf("expected provider persisted, got %q", cfg.Provider)
	}
	if cfg.Session.DefaultSession != "feature" {
		t.Fatalf("expected default session feature, got %q", cfg.Session.DefaultSession)
	}
}

func TestSaveLocalOverridesPreservesNestedSessionKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.local.json")
	writeFile(t, path, `{"session":{"enabled":true,"persist":true},"permissions":{"allow":["Read"]}}`)
	if err := config.SaveLocalOverrides(path, map[string]any{"session": map[string]any{"default_session": "main-2"}}); err != nil {
		t.Fatalf("SaveLocalOverrides = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings = %v", err)
	}
	content := string(data)
	for _, want := range []string{`"enabled": true`, `"persist": true`, `"default_session": "main-2"`, `"permissions"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %s in settings: %s", want, content)
		}
	}
}

func TestSaveLocalOverridesRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.local.json")
	writeFile(t, path, `{not json}`)
	if err := config.SaveLocalOverrides(path, map[string]any{"model": "opus"}); err == nil {
		t.Fatal("expected invalid existing JSON to error")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile(%q) = %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", path, err)
	}
}
