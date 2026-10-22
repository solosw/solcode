package config

import (
	"fmt"
	"slices"
	"strings"
)

// ModelEfforts returns the endpoint's explicitly declared capabilities. Unknown
// capabilities are nil, NOT a guessed list based on a model/provider name.
func (cfg Config) ModelEfforts() []string {
	if len(cfg.Providers) == 0 {
		return cfg.SupportedEfforts
	}
	for _, provider := range cfg.Providers {
		if cfg.Provider != "" && provider.Name != cfg.Provider {
			continue
		}
		for _, model := range provider.Models {
			if model.Name == cfg.Model || model.ID == cfg.Model {
				return model.SupportedEfforts
			}
		}
	}
	return nil
}

// WithModelEffort reconciles effort with known capabilities after model
// selection/restoration. An invalid preference uses the target model's default,
// then high if supported, then the first declared value. Unknown models keep
// existing legacy behavior but never advertise an unverified selector.
func (cfg Config) WithModelEffort(preferred string) Config {
	levels := cfg.ModelEfforts()
	if levels == nil {
		return cfg
	}
	if len(levels) == 0 {
		cfg.Effort, cfg.Thinking = "", false
		return cfg
	}
	if slices.Contains(levels, preferred) {
		cfg.Effort = preferred
	} else if !slices.Contains(levels, cfg.Effort) {
		if slices.Contains(levels, "high") {
			cfg.Effort = "high"
		} else {
			cfg.Effort = levels[0]
		}
	}
	return cfg
}

func validateEfforts(levels []string) error {
	seen := make(map[string]bool)
	for _, level := range levels {
		if strings.TrimSpace(level) == "" || strings.TrimSpace(level) != level || seen[level] {
			return fmt.Errorf("supported_efforts must contain unique, nonempty, trimmed values")
		}
		seen[level] = true
	}
	return nil
}
