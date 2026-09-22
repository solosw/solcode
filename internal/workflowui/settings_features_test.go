package workflowui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/workflow"
)

// settingsServer starts a server whose stored config is mutated by
// ApplySettings, mirroring how the real app persists and reloads.
func settingsServer(t *testing.T, initial config.Config) (*Server, string, *config.Config) {
	t.Helper()
	stored := initial
	applied := &stored
	srv, url, err := Start(Config{
		WorkDir: initial.WorkDir,
		List:    func() []workflow.Definition { return nil },
		Save: func(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error) {
			return "", nil
		},
		Settings: func() config.Config { return stored },
		ApplySettings: func(next config.Config) error {
			stored = next
			applied = &stored
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, url, applied
}

func decodeSettings(t *testing.T, url string) map[string]any {
	t.Helper()
	res, err := http.Get(url + "/api/settings")
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return body
}

func postSettings(t *testing.T, url string, payload map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(url+"/api/settings", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST settings: %v", err)
	}
	return res
}

// The settings payload must expose both new feature blocks so the UI can render
// them without a second endpoint.
func TestSettingsExposeComputerUseAndJev(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.ComputerUse.Enabled = true
	cfg.Jev = config.JevConfig{
		Enabled:            true,
		BaseURL:            "https://api.typesafe.ai",
		APIKeyEnv:          "TYPESAFE_API_KEY",
		APIKey:             "ts_secret",
		Model:              "jev-latest",
		TimeoutSec:         30,
		RouteMinConfidence: 0.7,
		Routing:            true,
		MemoryJudge:        true,
		Guardrail:          true,
	}
	_, url, _ := settingsServer(t, cfg)

	body := decodeSettings(t, url)

	computerUse, ok := body["computer_use"].(map[string]any)
	if !ok {
		t.Fatalf("computer_use = %#v", body["computer_use"])
	}
	if computerUse["enabled"] != true {
		t.Fatalf("computer_use.enabled = %v", computerUse["enabled"])
	}

	jev, ok := body["jev"].(map[string]any)
	if !ok {
		t.Fatalf("jev = %#v", body["jev"])
	}
	if jev["enabled"] != true || jev["routing"] != true || jev["guardrail"] != true {
		t.Fatalf("jev toggles = %#v", jev)
	}
	if jev["model"] != "jev-latest" {
		t.Fatalf("jev.model = %v", jev["model"])
	}
	if jev["route_min_confidence"] != 0.7 {
		t.Fatalf("jev.route_min_confidence = %v", jev["route_min_confidence"])
	}
	if jev["timeout_sec"] != float64(30) {
		t.Fatalf("jev.timeout_sec = %v", jev["timeout_sec"])
	}

	// The secret must never be echoed to the browser; only its presence.
	if jev["api_key_set"] != true {
		t.Fatalf("jev.api_key_set = %v", jev["api_key_set"])
	}
	if _, leaked := jev["api_key"]; leaked {
		t.Fatal("the Jev API key must not be sent to the client")
	}
	serialized, _ := json.Marshal(body)
	if bytes.Contains(serialized, []byte("ts_secret")) {
		t.Fatal("the settings response leaked the Jev API key")
	}
}

// Posting the toggles must apply them to the config the app will use.
func TestPostSettingsAppliesComputerUseAndJev(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	_, url, applied := settingsServer(t, cfg)

	res := postSettings(t, url, map[string]any{
		"computer_use_enabled":     true,
		"jev_enabled":              true,
		"jev_base_url":             "https://api.typesafe.ai",
		"jev_model":                "jev-latest",
		"jev_timeout_sec":          45,
		"jev_route_min_confidence": 0.75,
		"jev_routing":              true,
		"jev_memory_judge":         true,
		"jev_guardrail":            true,
		"jev_api_key_env":          "TYPESAFE_API_KEY",
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	if !applied.ComputerUse.Enabled {
		t.Fatal("computer_use was not applied")
	}
	if !applied.Jev.Enabled || !applied.Jev.Routing {
		t.Fatalf("jev = %+v", applied.Jev)
	}
	if applied.Jev.TimeoutSec != 45 {
		t.Fatalf("timeout = %d", applied.Jev.TimeoutSec)
	}
	if applied.Jev.RouteMinConfidence != 0.75 {
		t.Fatalf("route_min_confidence = %v", applied.Jev.RouteMinConfidence)
	}
	if !applied.Jev.MemoryJudge || !applied.Jev.Guardrail {
		t.Fatalf("jev subsystems = %+v", applied.Jev)
	}
	if applied.Jev.APIKeyEnv != "TYPESAFE_API_KEY" {
		t.Fatalf("api_key_env = %q", applied.Jev.APIKeyEnv)
	}
}

// A partial update must not reset fields it did not mention. This is what makes
// toggling one checkbox safe.
func TestPostSettingsDoesNotResetUnmentionedJevFields(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Jev = config.JevConfig{
		Enabled:            true,
		APIKey:             "ts_existing",
		BaseURL:            "https://api.typesafe.ai",
		Model:              "jev-latest",
		TimeoutSec:         30,
		RouteMinConfidence: 0.7,
		Routing:            true,
		MemoryJudge:        true,
		Guardrail:          false,
	}
	_, url, applied := settingsServer(t, cfg)

	// Toggle only the guardrail.
	res := postSettings(t, url, map[string]any{"jev_guardrail": true})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	if !applied.Jev.Guardrail {
		t.Fatal("guardrail should be enabled")
	}
	// Everything else must survive untouched, including the stored key.
	if applied.Jev.APIKey != "ts_existing" {
		t.Fatalf("api key was reset: %q", applied.Jev.APIKey)
	}
	if applied.Jev.Model != "jev-latest" || applied.Jev.TimeoutSec != 30 {
		t.Fatalf("jev = %+v", applied.Jev)
	}
	if applied.Jev.RouteMinConfidence != 0.7 || !applied.Jev.Routing || !applied.Jev.MemoryJudge {
		t.Fatalf("jev = %+v", applied.Jev)
	}
}

// A new key typed in the UI replaces the stored one.
func TestPostSettingsReplacesJevAPIKey(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Jev = config.JevConfig{Enabled: true, APIKey: "old-key"}
	_, url, applied := settingsServer(t, cfg)

	res := postSettings(t, url, map[string]any{"jev_api_key": "new-key"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if applied.Jev.APIKey != "new-key" {
		t.Fatalf("api key = %q, want the new key", applied.Jev.APIKey)
	}
}

// Omitting the key field leaves the stored key alone, which is what the UI
// relies on when the password field is left blank.
func TestPostSettingsKeepsJevAPIKeyWhenOmitted(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Jev = config.JevConfig{Enabled: true, APIKey: "keep-me"}
	_, url, applied := settingsServer(t, cfg)

	res := postSettings(t, url, map[string]any{"jev_routing": true})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if applied.Jev.APIKey != "keep-me" {
		t.Fatalf("api key = %q, want it preserved", applied.Jev.APIKey)
	}
}

// Disabling both features must round-trip to false.
func TestPostSettingsDisablesFeatures(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.ComputerUse.Enabled = true
	cfg.Jev = config.JevConfig{Enabled: true, APIKey: "k", Routing: true, Guardrail: true}
	_, url, applied := settingsServer(t, cfg)

	res := postSettings(t, url, map[string]any{
		"computer_use_enabled": false,
		"jev_enabled":          false,
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if applied.ComputerUse.Enabled {
		t.Fatal("computer_use should be disabled")
	}
	if applied.Jev.Enabled {
		t.Fatal("jev should be disabled")
	}
	// Disabling Jev does not wipe its configuration, so re-enabling is a
	// one-click action.
	if applied.Jev.APIKey != "k" || !applied.Jev.Routing {
		t.Fatalf("jev config was wiped: %+v", applied.Jev)
	}
}
