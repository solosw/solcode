package workflowui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/workflow"
)

// This exercises the same callbacks the real binary wires up: the settings POST
// persists through SaveLocalOverrides and then reloads, so the round trip
// through disk is covered rather than just the in-memory config mutation.
func TestSettingsPersistFeaturesThroughDisk(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.local.json")

	stored := config.Default()
	stored.WorkDir = dir
	if err := stored.Normalize(); err != nil {
		t.Fatal(err)
	}

	// apply mirrors cmd/solcode's ApplySettings: persist a nested object, then
	// adopt the new config.
	apply := func(next config.Config) error {
		if err := config.SaveLocalOverrides(settingsPath, map[string]any{
			"computer_use": map[string]any{"enabled": next.ComputerUse.Enabled},
			"jev": map[string]any{
				"enabled":              next.Jev.Enabled,
				"base_url":             next.Jev.BaseURL,
				"api_key":              next.Jev.APIKey,
				"api_key_env":          next.Jev.APIKeyEnv,
				"model":                next.Jev.Model,
				"timeout_sec":          next.Jev.TimeoutSec,
				"route_min_confidence": next.Jev.RouteMinConfidence,
				"routing":              next.Jev.Routing,
				"memory_judge":         next.Jev.MemoryJudge,
				"guardrail":            next.Jev.Guardrail,
			},
		}); err != nil {
			return err
		}
		stored = next
		return nil
	}

	srv, url, err := Start(Config{
		WorkDir: dir,
		List:    func() []workflow.Definition { return nil },
		Save: func(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error) {
			return "", nil
		},
		Settings:      func() config.Config { return stored },
		ApplySettings: apply,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	raw, _ := json.Marshal(map[string]any{
		"computer_use_enabled":     true,
		"jev_enabled":              true,
		"jev_api_key":              "ts_persisted",
		"jev_base_url":             "https://api.typesafe.ai",
		"jev_model":                "jev-latest",
		"jev_timeout_sec":          30,
		"jev_route_min_confidence": 0.7,
		"jev_routing":              true,
		"jev_memory_judge":         true,
		"jev_guardrail":            true,
	})
	res, err := http.Post(url+"/api/settings", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	// The settings file must now contain both blocks, nested as written.
	fileRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read persisted settings: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(fileRaw, &persisted); err != nil {
		t.Fatal(err)
	}
	cu, ok := persisted["computer_use"].(map[string]any)
	if !ok || cu["enabled"] != true {
		t.Fatalf("persisted computer_use = %#v", persisted["computer_use"])
	}
	jev, ok := persisted["jev"].(map[string]any)
	if !ok {
		t.Fatalf("persisted jev = %#v", persisted["jev"])
	}
	if jev["enabled"] != true || jev["routing"] != true || jev["guardrail"] != true {
		t.Fatalf("persisted jev = %#v", jev)
	}
	if jev["api_key"] != "ts_persisted" {
		t.Fatalf("persisted api key = %v", jev["api_key"])
	}

	// Loading the file back must produce a Jev that is actually live, which is
	// the check that the config key is parsed and not silently ignored.
	reloaded, err := config.Load(settingsPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.ComputerUseEnabled() {
		t.Fatal("reloaded ComputerUse should be enabled")
	}
	if !reloaded.JevEnabled() {
		t.Fatalf("reloaded Jev should be active: %+v", reloaded.Jev)
	}
	if !reloaded.Jev.Routing || !reloaded.Jev.MemoryJudge || !reloaded.Jev.Guardrail {
		t.Fatalf("reloaded jev subsystems = %+v", reloaded.Jev)
	}
	if reloaded.Jev.TimeoutSec != 30 || reloaded.Jev.RouteMinConfidence != 0.7 {
		t.Fatalf("reloaded jev = %+v", reloaded.Jev)
	}
}
