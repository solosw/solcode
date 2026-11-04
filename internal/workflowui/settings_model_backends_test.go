package workflowui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
)

func TestSettingsIndependentAPIModels(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	_, url, applied := settingsServer(t, cfg)
	response := postSettings(t, url, map[string]any{
		"subagent_model":           " child-model ",
		"memory_organizer_enabled": true, "memory_organizer_runtime": "api",
		"memory_organizer_model": "vendor/summary", "memory_organizer_api_key": "organizer-secret",
		"memory_organizer_base_url": "https://organizer.example.test/v1/", "memory_organizer_api_format": "responses",
		"embedding_enabled": true, "embedding_type": "api", "embedding_model": "vendor/embedding",
		"embedding_api_key": "embedding-secret", "embedding_base_url": "https://embedding.example.test/v1/",
	})
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("POST = %d", response.StatusCode)
	}
	if applied.Subagents.Model != "child-model" || !applied.OrganizerEnabled() || !applied.EmbeddingEnabled() {
		t.Fatalf("settings not applied")
	}
	if applied.Memory.Organizer.Model != "vendor/summary" || applied.Memory.Organizer.ModelPath != "" {
		t.Fatal("API id rewritten as local model")
	}
	settings := decodeSettings(t, url)
	if settings["subagent_model"] != "child-model" {
		t.Fatal("GET omitted the user subagent model")
	}
	public, _ := json.Marshal(settings)
	if strings.Contains(string(public), "organizer-secret") || strings.Contains(string(public), "embedding-secret") {
		t.Fatal("GET exposed an API key")
	}
	body := buildMemoryOrganizerSettings(*applied)
	raw, _ := json.Marshal(body)
	if !body.APIKeySet || body.APIFormat != "responses" || body.LibraryPresent || body.ModelPresent || strings.Contains(string(raw), "organizer-secret") {
		t.Fatalf("unsafe or incorrect settings response: %s", raw)
	}
	response = postSettings(t, url, map[string]any{"subagent_model": "another-model"})
	response.Body.Close()
	if response.StatusCode != 200 || applied.Subagents.Model != "another-model" {
		t.Fatal("partial update failed")
	}
	if applied.Memory.Organizer.APIKey != "organizer-secret" {
		t.Fatal("partial update cleared API key")
	}
}
