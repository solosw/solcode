package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeDiscoveryConfig(t *testing.T, providers []ProviderConfig, model string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"provider": providers[0].Name, "model": model, "providers": providers})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStartupDiscoversModelsForAllProvidersAndPrefersLocal(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer env-key" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"alias","display_name":"remote collision"},{"id":"local-id"},{"id":"remote-only","display_name":"New Model"},{"id":"remote-only"},{"id":""}]}`))
	}))
	defer server.Close()
	var otherCalls atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") != "native-key" || r.Header.Get("anthropic-version") == "" {
			t.Error("wrong native models request")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"remote-only"}]}`))
	}))
	defer other.Close()
	t.Setenv("SOLCODE_DISCOVERY_URL", server.URL+"/v1")
	t.Setenv("SOLCODE_DISCOVERY_KEY", "env-key")
	output := int64(1234)
	thinking := false
	local := ModelConfig{Name: "alias", ID: "local-id", DisplayName: "Local Label", Default: true, MaxContextTokens: 123456, MaxTokens: &output, Thinking: &thinking, SupportedEfforts: []string{}, APIParams: map[string]json.RawMessage{"custom": json.RawMessage("true")}}
	path := writeDiscoveryConfig(t, []ProviderConfig{{Name: "first", BaseURLEnv: "SOLCODE_DISCOVERY_URL", APIKeyEnv: "SOLCODE_DISCOVERY_KEY", APIFormat: "openai", Models: []ModelConfig{local}}, {Name: "other", BaseURL: other.URL, APIFormat: "anthropic", APIKey: "native-key"}}, "alias")
	for startup := 1; startup <= 2; startup++ {
		cfg, err := LoadForStartup(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != int32(startup) || otherCalls.Load() != int32(startup) {
			t.Fatal("catalog not refreshed for every provider at startup")
		}
		got := cfg.Providers[0].Models
		local.Provider = "first"
		if len(got) != 2 || !reflect.DeepEqual(got[0], local) || got[1].ID != "remote-only" || got[1].DisplayName != "New Model" {
			t.Fatalf("merged=%+v", got)
		}
		if cfg.Model != "local-id" || cfg.MaxTokens != 1234 || cfg.Thinking || cfg.MaxContextTokens != 123456 {
			t.Fatal("remote overwrote local settings")
		}
		if len(cfg.ListModels()) != 2 || len(cfg.Providers[1].Models) != 1 {
			t.Fatal("models unavailable in runtime catalogs")
		}
		selected, err := cfg.WithModel("remote-only")
		if err != nil || selected.Model != "remote-only" {
			t.Fatalf("remote selection=%q error=%v", selected.Model, err)
		}
		selected, err = selected.WithProvider("other")
		if err != nil || selected.Model != "remote-only" || selected.BaseURL != other.URL {
			t.Fatal("remote provider selection failed", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		savePath := filepath.Join(t.TempDir(), "settings.local.json")
		if err := SaveLocalOverrides(savePath, map[string]any{"providers": cfg.Providers}); err != nil {
			t.Fatal(err)
		}
		saved, err := os.ReadFile(savePath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(saved), "remote-only") {
			t.Fatal("discovered catalog leaked into persistent settings")
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("startup modified local settings")
		}
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || otherCalls.Load() != 2 {
		t.Fatal("ordinary config load performed network discovery")
	}
}

func TestModelContextWindowPrecedence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openai/gpt-5","context_length":999999}, {"id":"gpt-5-mini","context_window":400000},
			{"id":"anthropic/Claude-Sonnet","context_length":200000},
			{"id":"google/gemini-3"},{"id":"deepseek-ai/DeepSeek-V3"},
			{"id":"glm-5"},{"id":"kimi-k2"},{"id":"grok-4"},{"id":"Qwen3.5"},
			{"id":"custom","max_context_tokens":{},"context_length":"90000"},
			{"id":"private","max_model_len":64000},
			{"id":"invalid","context_window":-1,"max_output_tokens":1000}
		]}`))
	}))
	defer server.Close()
	path := writeDiscoveryConfig(t, []ProviderConfig{{Name: "test", BaseURL: server.URL, Models: []ModelConfig{
		{Name: "manual", ID: "openai/gpt-5", MaxContextTokens: 123000},
		{Name: "alias", ID: "private"},
	}}}, "manual")
	cfg, err := LoadForStartup(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model string
		want  int64
	}{
		{"manual", 123000}, {"gpt-5-mini", 256000}, {"anthropic/Claude-Sonnet", 500000}, {"google/gemini-3", 500000},
		{"deepseek-ai/DeepSeek-V3", 1000000}, {"glm-5", 1000000}, {"kimi-k2", 1000000},
		{"grok-4", 200000}, {"Qwen3.5", 256000}, {"custom", 90000}, {"alias", 64000}, {"invalid", 200000},
	} {
		if got := cfg.ContextLimitForModel(tc.model); got != tc.want {
			t.Errorf("catalog window for %s = %d, want %d", tc.model, got, tc.want)
		}
		cfg, err = cfg.WithModel(tc.model)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MaxContextTokens != tc.want {
			t.Errorf("active window for %s = %d, want %d", tc.model, cfg.MaxContextTokens, tc.want)
		}
	}

	// Existing settings can have a top-level value written by the settings UI.
	// Only model-level overrides apply when a provider catalog is configured.
	if err := applyJSONConfig(&cfg, []byte(`{"max_context_tokens":200000}`)); err != nil {
		t.Fatal(err)
	}
	selected, err := cfg.WithModel("google/gemini-3")
	if err != nil || selected.MaxContextTokens != 500000 {
		t.Fatalf("Gemini window = %d, error=%v", selected.MaxContextTokens, err)
	}
}

func TestLegacyContextWindowOverride(t *testing.T) {
	cfg := Default()
	if err := applyJSONConfig(&cfg, []byte(`{"model":"gpt-5","max_context_tokens":200000}`)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.MaxContextTokens != 200000 || cfg.LegacyContextLimitOverride() != 200000 {
		t.Fatalf("legacy window = %d, override = %d", cfg.MaxContextTokens, cfg.LegacyContextLimitOverride())
	}
	cfg.MaxContextTokens = 0
	next, err := cfg.WithModel("claude-sonnet")
	if err != nil || next.MaxContextTokens != 500000 || next.LegacyContextLimitOverride() != 0 {
		t.Fatalf("automatic legacy window = %d, override = %d, error=%v", next.MaxContextTokens, next.LegacyContextLimitOverride(), err)
	}
}

func TestStartupResolvesRemoteOnlyProviderBeforeNormalize(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"id":"first"},{"id":"selected"}]}`))
	}))
	defer server.Close()
	path := writeDiscoveryConfig(t, []ProviderConfig{{Name: "mine", BaseURL: server.URL, APIFormat: "responses"}}, "selected")
	cfg, err := LoadForStartup(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "selected" || len(cfg.ListModels()) != 2 || calls.Load() != 1 {
		t.Fatalf("model=%q calls=%d", cfg.Model, calls.Load())
	}
	cfg.Providers[0].Models[1].KeepLocal()
	cfg.Providers[0].Models[1].MaxContextTokens = 654321
	raw, err := json.Marshal(cfg.Providers[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "selected") || strings.Contains(string(raw), `"id":"first"`) {
		t.Fatal("edited remote model not persisted as local override")
	}
}

func TestDiscoveryFailureKeepsLocalModels(t *testing.T) {
	for _, failure := range []string{"status", "malformed", "empty", "timeout"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch failure {
				case "status":
					http.Error(w, "unsupported", 404)
				case "malformed":
					_, _ = w.Write([]byte("not json"))
				case "empty":
					_, _ = w.Write([]byte(`{"data":[]}`))
				case "timeout":
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			cfg := Default()
			cfg.Providers = []ProviderConfig{{Name: "mine", BaseURL: server.URL, APIFormat: "openai", Models: []ModelConfig{{Name: "local", ID: "local", MaxContextTokens: 999}}}}
			original := append([]ModelConfig(nil), cfg.Providers[0].Models...)
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			cfg.discoverProviderModels(ctx)
			if !reflect.DeepEqual(cfg.Providers[0].Models, original) {
				t.Fatal("discovery failure changed local models")
			}
		})
	}
}

func TestProviderModelsEndpoint(t *testing.T) {
	for _, base := range []string{"https://example.test", "https://example.test/", "https://example.test/v1", "https://example.test/v1/", "https://example.test/v1/messages", "https://example.test/v1/chat/completions", "https://example.test/v1/responses", "https://example.test/v1/models"} {
		got, err := providerModelsEndpoint(base, "openai")
		if err != nil || got != "https://example.test/v1/models" {
			t.Fatalf("base=%q got=%q err=%v", base, got, err)
		}
	}
	got, err := providerModelsEndpoint("https://example.test/api/v1/chat/completions?tenant=one", "openai")
	if err != nil || got != "https://example.test/api/v1/models?tenant=one" {
		t.Fatalf("prefixed URL=%q error=%v", got, err)
	}
}
