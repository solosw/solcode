package config

import "testing"

func TestContextLimitForModel(t *testing.T) {
	cfg := Default()
	cfg.Provider = "primary"
	cfg.Model = "main-id"
	cfg.FastModel = "child-id"
	cfg.MaxContextTokens = 200000
	cfg.Providers = []ProviderConfig{
		{Name: "other", Models: []ModelConfig{{ID: "child-id", MaxContextTokens: 999999}}},
		{Name: "primary", Models: []ModelConfig{{Name: "child", ID: "child-id", MaxContextTokens: 32000}, {Name: "no-limit", ID: "no-limit-id"}}},
	}
	for _, tc := range []struct {
		model string
		want  int64
	}{{"", 200000}, {"main-id", 200000}, {"child-id", 32000}, {"child", 32000}, {"fast", 32000}, {"unknown", 200000}, {"no-limit-id", 200000}} {
		if got := cfg.ContextLimitForModel(tc.model); got != tc.want {
			t.Errorf("%s capacity=%d want=%d", tc.model, got, tc.want)
		}
	}
}
