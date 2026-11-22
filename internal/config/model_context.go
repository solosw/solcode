package config

import "strings"

const defaultContextTokens int64 = 200_000

func builtinContextTokens(model string) int64 {
	model = strings.ToLower(strings.TrimSpace(model))
	model = model[strings.LastIndexByte(model, '/')+1:]
	switch {
	case strings.HasPrefix(model, "gpt"), strings.HasPrefix(model, "chatgpt"), strings.HasPrefix(model, "qwen"):
		return 256_000
	case strings.HasPrefix(model, "claude"), strings.HasPrefix(model, "gemini"):
		return 500_000
	case strings.HasPrefix(model, "deepseek"), strings.HasPrefix(model, "glm"), strings.HasPrefix(model, "kimi"):
		return 1_000_000
	case strings.HasPrefix(model, "grok"):
		return 200_000
	default:
		return 0
	}
}

func (cfg Config) resolveModelContextTokens(model ModelConfig) int64 {
	if model.MaxContextTokens > 0 {
		return model.MaxContextTokens
	}
	if len(cfg.Providers) == 0 && cfg.manualContextTokens > 0 {
		return cfg.manualContextTokens
	}
	if tokens := builtinContextTokens(model.ID); tokens > 0 {
		return tokens
	}
	if model.remoteContextTokens > 0 {
		return model.remoteContextTokens
	}
	return defaultContextTokens
}

// LegacyContextLimitOverride returns the top-level override for a legacy endpoint.
// Configured providers store overrides on individual models.
func (cfg Config) LegacyContextLimitOverride() int64 {
	if len(cfg.Providers) > 0 {
		return 0
	}
	if cfg.lastResolvedContextTokens == 0 {
		if cfg.MaxContextTokens > 0 && cfg.MaxContextTokens != defaultContextTokens {
			return cfg.MaxContextTokens
		}
	} else if cfg.MaxContextTokens != cfg.lastResolvedContextTokens {
		return cfg.MaxContextTokens
	}
	return cfg.manualContextTokens
}

// ContextLimitForModel resolves capacity using the active provider catalog.
func (cfg Config) ContextLimitForModel(model string) int64 {
	model = strings.TrimSpace(model)
	if strings.EqualFold(model, "fast") {
		model = strings.TrimSpace(cfg.FastModel)
	}
	if model == "" {
		model = cfg.Model
	}
	providerName := cfg.Provider
	if providerName == "" && len(cfg.Providers) > 0 {
		if provider, _, err := cfg.resolveActive(); err == nil {
			providerName = provider.Name
		}
	}
	for _, candidate := range cfg.modelsOf(providerName) {
		if candidate.ID == model || candidate.Name == model {
			return cfg.resolveModelContextTokens(candidate)
		}
	}
	if model == cfg.Model && cfg.MaxContextTokens > 0 {
		return cfg.MaxContextTokens
	}
	return cfg.resolveModelContextTokens(ModelConfig{ID: model})
}
