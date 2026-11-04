package config

import (
	"path/filepath"
	"testing"
)

func TestOrganizerAPINormalizationAndIsolation(t *testing.T) {
	t.Setenv("ORG_KEY", "organizer-secret")
	t.Setenv("ORG_URL", "https://organizer.example.test/v1/")
	cfg := Default()
	cfg.APIKey = "chat-key"
	cfg.BaseURL = "https://chat.example.test"
	cfg.Memory.Organizer = OrganizerConfig{Enabled: true, Runtime: " API ", Model: "vendor/summary-model", APIKeyEnv: "ORG_KEY", BaseURLEnv: "ORG_URL", ModelPath: "old.gguf"}
	cfg.NormalizeLocalModels()
	org := cfg.Memory.Organizer
	if !cfg.OrganizerEnabled() || cfg.OrganizerType() != OrganizerRuntimeAPI {
		t.Fatal("API not enabled")
	}
	if org.Model != "vendor/summary-model" || org.ModelPath != "" {
		t.Fatalf("API model rewritten: %+v", org)
	}
	if org.APIKey != "organizer-secret" || org.BaseURL != "https://organizer.example.test/v1" || org.APIFormat != "openai" {
		t.Fatalf("wrong endpoint settings: %+v", org)
	}
	for _, missing := range []string{"key", "url", "model"} {
		copy := cfg
		switch missing {
		case "key":
			copy.Memory.Organizer.APIKey = ""
		case "url":
			copy.Memory.Organizer.BaseURL = ""
		case "model":
			copy.Memory.Organizer.Model = ""
		}
		if copy.OrganizerEnabled() {
			t.Fatalf("enabled without %s", missing)
		}
	}
	cfg.Memory.Organizer = OrganizerConfig{Enabled: true, Runtime: "api", Model: "summary"}
	cfg.NormalizeLocalModels()
	if cfg.OrganizerEnabled() || cfg.Memory.Organizer.APIKey != "" || cfg.Memory.Organizer.BaseURL != "" {
		t.Fatal("inherited chat credentials")
	}
}

func TestModelBackendConfigLayer(t *testing.T) {
	cfg := Default()
	err := applyJSONConfig(&cfg, []byte(`{"subagents":{"model":"child-model"},"memory":{"organizer":{"runtime":"api","model":"vendor/summary","base_url":"https://organizer.example.test/v1","api_key":"key"},"embedding":{"enabled":true,"type":"api","model":"vendor/embedding","api_key":"embedding-key","base_url":"https://embedding.example.test/v1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.NormalizeLocalModels()
	if cfg.Subagents.Model != "child-model" {
		t.Fatalf("subagent model = %s", cfg.Subagents.Model)
	}
	if cfg.Memory.Organizer.Model != "vendor/summary" || cfg.Memory.Embedding.Model != "vendor/embedding" || !cfg.EmbeddingEnabled() {
		t.Fatal("API models not preserved")
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := SaveLocalOverrides(path, map[string]any{"subagents": cfg.Subagents, "memory": cfg.Memory}); err != nil {
		t.Fatal(err)
	}
	reloaded := Default()
	if err := loadOptionalFile(&reloaded, path); err != nil {
		t.Fatal(err)
	}
	reloaded.NormalizeLocalModels()
	if reloaded.Subagents.Model != cfg.Subagents.Model || reloaded.Memory.Organizer.Model != cfg.Memory.Organizer.Model || reloaded.Memory.Organizer.APIKey != cfg.Memory.Organizer.APIKey || reloaded.Memory.Embedding.Model != cfg.Memory.Embedding.Model {
		t.Fatal("API model settings did not survive disk round trip")
	}
}
