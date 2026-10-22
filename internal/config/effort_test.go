package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExplicitEffortCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		levels                   []string
		current, preferred, want string
		thinking                 bool
	}{
		{"compatibility defaults", nil, "high", "max", "max", true},
		{"disabled", []string{}, "high", "max", "", false},
		{"preserve supported preference", []string{"low", "max"}, "low", "max", "max", true},
		{"target default", []string{"low", "max"}, "low", "high", "low", true},
		{"high fallback", []string{"low", "high"}, "max", "max", "high", true},
		{"first fallback", []string{"low", "medium"}, "max", "high", "low", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.SupportedEfforts, cfg.Effort = tc.levels, tc.current
			got := cfg.WithModelEffort(tc.preferred)
			if got.Effort != tc.want || got.Thinking != tc.thinking {
				t.Fatalf("effort=%q thinking=%v", got.Effort, got.Thinking)
			}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Config
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded.SupportedEfforts, tc.levels) {
				t.Fatalf("lost nil/empty distinction: %s", data)
			}
		})
	}
}

func TestEffortCapabilitiesArePerModel(t *testing.T) {
	cfg := Default()
	cfg.Provider, cfg.Model = "b", "same-id"
	cfg.Providers = []ProviderConfig{
		{Name: "a", Models: []ModelConfig{{Name: "same", ID: "same-id", SupportedEfforts: []string{"high"}}}},
		{Name: "b", Models: []ModelConfig{{Name: "same", ID: "same-id", SupportedEfforts: []string{"low"}}}},
	}
	if got := cfg.ModelEfforts(); !reflect.DeepEqual(got, []string{"low"}) {
		t.Fatalf("wrong provider capabilities: %v", got)
	}
	cfg.Model = "unknown"
	if cfg.ModelEfforts() != nil {
		t.Fatal("unknown model must not inherit another model's capabilities")
	}
}

func TestInvalidLegacyEffortCapabilities(t *testing.T) {
	for _, levels := range [][]string{{""}, {"high", "high"}, {" high"}} {
		cfg := Default()
		cfg.SupportedEfforts = levels
		if err := cfg.Normalize(); err == nil {
			t.Fatalf("accepted invalid levels %v", levels)
		}
	}
}

func TestProviderSwitchDoesNotInheritPreviousWireFormat(t *testing.T) {
	for _, global := range []string{"", "openai"} {
		cfg := Default()
		cfg.APIFormat = global
		cfg.Providers = []ProviderConfig{
			{Name: "implicit", Models: []ModelConfig{{Name: "same", ID: "same"}}},
			{Name: "explicit", APIFormat: "responses", Models: []ModelConfig{{Name: "same", ID: "same"}}},
		}
		cfg.Provider, cfg.Model = "implicit", "same"
		if err := cfg.Normalize(); err != nil {
			t.Fatal(err)
		}
		expected := cfg.APIFormat
		next, err := cfg.WithProvider("explicit")
		if err != nil {
			t.Fatal(err)
		}
		if next.APIFormat != "responses" {
			t.Fatalf("explicit format not applied: %s", next.APIFormat)
		}
		next, err = next.WithProvider("implicit")
		if err != nil {
			t.Fatal(err)
		}
		if next.APIFormat != expected {
			t.Fatalf("global=%q: inherited %q instead of %q", global, next.APIFormat, expected)
		}
	}
}

func TestModelSwitchRestoresGlobalThinkingDefault(t *testing.T) {
	for _, thinking := range []bool{true, false} {
		cfg := Default()
		cfg.Thinking = thinking
		cfg.Providers = []ProviderConfig{{Name: "p", Models: []ModelConfig{
			{Name: "reasoning", ID: "reasoning", SupportedEfforts: []string{"high"}},
			{Name: "plain", ID: "plain", SupportedEfforts: []string{}},
		}}}
		cfg.Provider, cfg.Model = "p", "reasoning"
		if err := cfg.Normalize(); err != nil {
			t.Fatal(err)
		}
		plain, err := cfg.WithModel("plain")
		if err != nil {
			t.Fatal(err)
		}
		plain = plain.WithModelEffort(cfg.Effort)
		if plain.Thinking {
			t.Fatal("explicitly non-reasoning model enabled thinking")
		}
		restored, err := plain.WithModel("reasoning")
		if err != nil {
			t.Fatal(err)
		}
		restored = restored.WithModelEffort(plain.Effort)
		if restored.Thinking != thinking {
			t.Fatalf("thinking=%v, want global default %v", restored.Thinking, thinking)
		}
	}
}

func TestRuntimeJSONUpdatesModelFallbackDefaults(t *testing.T) {
	cfg := Default()
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := applyJSONConfig(&cfg, []byte(`{"api_format":"openai","thinking":false}`)); err != nil {
		t.Fatal(err)
	}
	next, err := cfg.WithModel("another-model")
	if err != nil {
		t.Fatal(err)
	}
	if next.APIFormat != "openai" || next.Thinking {
		t.Fatalf("updated defaults not applied: format=%s thinking=%v", next.APIFormat, next.Thinking)
	}
}

func TestModelEffortCompatibilityDefaults(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   []string
	}{
		{"anthropic", []string{"low", "medium", "high", "max"}},
		{"openai", []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"responses", []string{"none", "low", "medium", "high", "xhigh", "max"}},
	} {
		t.Run(tc.format, func(t *testing.T) {
			cfg := Default()
			// Provider override must win over the top-level wire format.
			cfg.APIFormat = "anthropic"
			cfg.Provider, cfg.Model = "p", "alias"
			cfg.Providers = []ProviderConfig{{Name: "p", APIFormat: tc.format, Models: []ModelConfig{{Name: "alias", ID: "custom-id", Effort: "high"}}}}
			if cfg.ModelEffortsExplicit() {
				t.Fatal("compatibility defaults reported as verified metadata")
			}
			if got := cfg.ModelEfforts(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("default levels = %v, want %v", got, tc.want)
			}
			// Resolved model IDs work just like configured aliases.
			cfg.Model = "custom-id"
			if got := cfg.ModelEfforts(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resolved ID lost its levels: %v", got)
			}
			got := cfg.ModelEfforts()
			got[0] = "mutated"
			if !reflect.DeepEqual(cfg.ModelEfforts(), tc.want) || cfg.Providers[0].Models[0].SupportedEfforts != nil {
				t.Fatal("reading defaults mutated or filled user settings")
			}
		})
	}
}

func TestModelEffortExplicitOverridesAndThinkingDisabled(t *testing.T) {
	cfg := Default()
	cfg.APIFormat = "responses"
	cfg.SupportedEfforts = []string{"minimal", "custom"}
	if !cfg.ModelEffortsExplicit() || !reflect.DeepEqual(cfg.ModelEfforts(), cfg.SupportedEfforts) {
		t.Fatal("explicit list did not replace defaults")
	}
	got := cfg.ModelEfforts()
	got[0] = "mutated"
	if cfg.SupportedEfforts[0] != "minimal" {
		t.Fatal("caller mutated declared capabilities")
	}
	cfg.SupportedEfforts = []string{}
	if !cfg.ModelEffortsExplicit() || cfg.ModelEfforts() == nil || len(cfg.ModelEfforts()) != 0 {
		t.Fatal("explicit empty list was replaced with defaults")
	}
	cfg.SupportedEfforts = nil
	cfg.Thinking = false
	if cfg.ModelEfforts() != nil {
		t.Fatal("thinking-disabled legacy endpoint got fallback levels")
	}
	thinking := false
	cfg.Thinking = true
	cfg.Provider, cfg.Model = "p", "plain"
	cfg.Providers = []ProviderConfig{{Name: "p", APIFormat: "responses", Models: []ModelConfig{{Name: "plain", ID: "plain", Thinking: &thinking}}}}
	if cfg.ModelEfforts() != nil {
		t.Fatal("thinking-disabled model got fallback levels")
	}
}

func TestModelEffortCompatibilityPreservesCustomDefault(t *testing.T) {
	cfg := Default()
	cfg.Effort = "provider-custom"
	if got := cfg.WithModelEffort(cfg.Effort); got.Effort != cfg.Effort {
		t.Fatal("compatibility defaults rewrote a legacy custom effort")
	}
	cfg.Provider, cfg.Model = "p", "custom"
	cfg.Providers = []ProviderConfig{{Name: "p", Models: []ModelConfig{
		{Name: "custom", ID: "custom", Effort: "provider-custom"},
		{Name: "other", ID: "other", Effort: "high"},
	}}}
	if got := cfg.WithModelEffort(cfg.Effort); got.Effort != cfg.Effort {
		t.Fatal("compatibility defaults rewrote a model-specific custom effort")
	}
	cfg.Model = "other"
	for _, effort := range cfg.ModelEfforts() {
		if effort == "provider-custom" {
			t.Fatal("another model inherited a custom capability")
		}
	}
}
