package config

import "strings"

// ContextLimitForModel resolves child capacity only from the active provider.
// Unknown models fall back to the active context limit rather than guessing
// provider capabilities from a model name.
func (cfg Config) ContextLimitForModel(model string) int64 {
	model = strings.TrimSpace(model)
	if strings.EqualFold(model, "fast") {
		model = strings.TrimSpace(cfg.FastModel)
	}
	if model == "" || model == cfg.Model {
		return cfg.MaxContextTokens
	}
	for _, provider := range cfg.Providers {
		if provider.Name != cfg.Provider {
			continue
		}
		for _, candidate := range provider.Models {
			if (candidate.ID == model || candidate.Name == model) && candidate.MaxContextTokens > 0 {
				return candidate.MaxContextTokens
			}
		}
	}
	return cfg.MaxContextTokens
}
