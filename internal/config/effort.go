package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/solosw/solcode/internal/anthropic"
)

// modelEffortConfig resolves metadata only within the active provider. The
// legacy single-model endpoint is represented the same way as a catalog model.
func (cfg Config) modelEffortConfig() (ModelConfig, string, bool) {
	if len(cfg.Providers) == 0 {
		return ModelConfig{SupportedEfforts: cfg.SupportedEfforts, Effort: cfg.Effort}, cfg.APIFormat, true
	}
	for _, provider := range cfg.Providers {
		if cfg.Provider != "" && provider.Name != cfg.Provider {
			continue
		}
		for _, model := range provider.Models {
			if model.Name == cfg.Model || model.ID == cfg.Model {
				format := cfg.APIFormat
				if provider.APIFormat != "" {
					format = provider.APIFormat
				}
				return model, format, true
			}
		}
	}
	return ModelConfig{}, "", false
}

// ModelEffortsExplicit distinguishes endpoint-declared capabilities (including
// [] to disable selection) from our compatibility defaults. ACP exposes that
// distinction in the selector description; defaults are not upstream discovery.
func (cfg Config) ModelEffortsExplicit() bool {
	model, _, found := cfg.modelEffortConfig()
	return found && model.SupportedEfforts != nil
}

// ModelEfforts prefers model-specific capabilities. For existing settings that
// omit them, expose compatibility choices rather than hiding the ACP control.
// As in BaoCode's native model settings, these defaults are UI conventions, not
// a guarantee that every upstream implements every level. An explicit list
// replaces them, and an explicit empty list disables selection.
func (cfg Config) ModelEfforts() []string {
	model, format, found := cfg.modelEffortConfig()
	if !found {
		return nil
	}
	if model.SupportedEfforts != nil {
		return slices.Clone(model.SupportedEfforts)
	}
	thinking := cfg.Thinking
	if model.Thinking != nil {
		thinking = *model.Thinking
	}
	if !thinking {
		return nil // Do not opt a non-thinking model into adjustable reasoning.
	}
	var levels []string
	switch anthropic.NormalizeFormat(format) {
	case anthropic.FormatOpenAI, anthropic.FormatResponses:
		// BaoCode ProviderModel.defaultEfforts. Preserve these values verbatim;
		// in particular max is not silently translated into xhigh.
		levels = []string{"none", "low", "medium", "high", "xhigh", "max"}
	default:
		// Anthropic uses adaptive thinking + output_config.effort; do not send
		// an implicit "none" or "xhigh" to that different wire protocol.
		levels = []string{"low", "medium", "high", "max"}
	}
	// Existing custom defaults must not be silently rewritten on session/new.
	// Use this model's configured default, never another model's capabilities.
	if effort := strings.TrimSpace(model.Effort); effort != "" && !slices.Contains(levels, effort) {
		levels = append(levels, effort)
	}
	return levels
}

// WithModelEffort reconciles effort after model selection/restoration. An invalid
// preference uses the target model's default, then high, then the first level.
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
