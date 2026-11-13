package config

// ToolSearchConfig controls on-demand tool discovery. Omitted enabled preserves
// the compact catalog; false sends all enabled, model-visible tools directly.
type ToolSearchConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (cfg Config) ToolSearchEnabled() bool {
	return cfg.ToolSearch.Enabled == nil || *cfg.ToolSearch.Enabled
}
