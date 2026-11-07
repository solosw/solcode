package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadContextNudgeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"context_nudge":{"min_pressure":0.3,"frequency":9,"growth_tokens":30000,"min_benefit_tokens":2500}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.ContextNudge.Defaults()
	if c.MinPressure != .3 || c.MaxPressure != .75 || c.EmergencyPressure != .95 || c.Frequency != 9 || c.GrowthTokens != 30000 || c.MinBenefitTokens != 2500 {
		t.Fatalf("settings not loaded: %+v", c)
	}
}
