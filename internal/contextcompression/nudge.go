package contextcompression

// NudgeConfig adapts the upstream pressure bands and suppression controls to
// SolCode's local request estimate. Zero-valued fields use the defaults below.
type NudgeConfig struct {
	MinPressure        float64 `json:"min_pressure,omitempty"`
	MaxPressure        float64 `json:"max_pressure,omitempty"`
	EmergencyPressure  float64 `json:"emergency_pressure,omitempty"`
	Frequency          int     `json:"frequency,omitempty"`
	IterationThreshold int     `json:"iteration_threshold,omitempty"`
	GrowthTokens       int     `json:"growth_tokens,omitempty"`
	MinGrowthTokens    int     `json:"min_growth_tokens,omitempty"`
	MinBenefitTokens   int     `json:"min_benefit_tokens,omitempty"`
}

func (c NudgeConfig) Defaults() NudgeConfig {
	if !(c.MinPressure > 0 && c.MinPressure < 1) {
		c.MinPressure = .45
	}
	if !(c.MaxPressure >= c.MinPressure && c.MaxPressure < 1) {
		c.MaxPressure = max(.75, c.MinPressure)
	}
	if !(c.EmergencyPressure >= c.MaxPressure && c.EmergencyPressure <= 1) {
		c.EmergencyPressure = max(.95, c.MaxPressure)
	}
	if c.Frequency <= 0 {
		c.Frequency = 5
	}
	if c.IterationThreshold <= 0 {
		c.IterationThreshold = 15
	}
	if c.GrowthTokens <= 0 {
		c.GrowthTokens = 50_000
	}
	if c.MinGrowthTokens <= 0 {
		c.MinGrowthTokens = 20_000
	}
	if c.MinBenefitTokens <= 0 {
		c.MinBenefitTokens = 5_000
	}
	return c
}

// NudgeState is session-local and checkpointed with the active view, so starting
// a new user turn does not reset cooldown or accumulated growth.
type NudgeState struct {
	Budget         int    `json:"budget,omitempty"`
	Requests       int    `json:"requests"`
	LastRequest    int    `json:"last_request"`
	BaselineTokens int    `json:"baseline_tokens"`
	LastLevel      string `json:"last_level,omitempty"`
	Initialized    bool   `json:"initialized,omitempty"`
}

// Decide advances once at a model boundary. Emergency escalation can bypass
// cooldown/growth/benefit suppression, but never invents a compressible range.
func (n *NudgeState) Decide(c NudgeConfig, tokens, maxTokens int, overview Overview) string {
	c = c.Defaults()
	n.Requests++
	if !n.Initialized || n.Budget != maxTokens {
		n.Budget = maxTokens
		n.LastLevel = ""
		n.Initialized = true
		n.BaselineTokens = tokens
		n.LastRequest = n.Requests
	}
	if maxTokens <= 0 || overview.TotalCompressibleRanges == 0 || overview.EstimatedSavings <= 0 {
		return ""
	}
	pressure := float64(tokens) / float64(maxTokens)
	if pressure < c.MinPressure {
		return ""
	}
	elapsed, growth := n.Requests-n.LastRequest, tokens-n.BaselineTokens
	level := "growth"
	if pressure >= c.EmergencyPressure {
		level = "emergency"
		if n.LastLevel == level && (elapsed < c.Frequency || growth <= 0) {
			return ""
		}
	} else {
		if (n.LastLevel != "" && elapsed < c.Frequency) || overview.EstimatedSavings < c.MinBenefitTokens {
			return ""
		}
		if pressure >= c.MaxPressure {
			level = "pressure"
			// The first pressure warning needs no prior growth. Subsequent ones
			// wait for meaningful growth, even across separate user turns.
			if n.LastLevel != "" && growth < min(c.MinGrowthTokens, int(float64(maxTokens)*.45)) {
				return ""
			}
		} else if growth < c.GrowthTokens || elapsed < c.IterationThreshold {
			return ""
		}
	}
	n.LastRequest, n.BaselineTokens, n.LastLevel = n.Requests, tokens, level
	return level
}

func (n *NudgeState) Compressed(tokens int) {
	n.Initialized = true
	n.LastRequest = n.Requests
	n.BaselineTokens = tokens
	n.LastLevel = "compressed"
}
