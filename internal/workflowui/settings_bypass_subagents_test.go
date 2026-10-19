package workflowui

import (
	"net/http"
	"testing"

	"github.com/solosw/solcode/internal/config"
)

func TestSettingsBypassSubagentsToggle(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	_, url, applied := settingsServer(t, cfg)
	if got := decodeSettings(t, url)["bypass_subagents_enabled"]; got != false {
		t.Fatalf("default bypass_subagents_enabled = %v, want false", got)
	}
	for _, enabled := range []bool{true, false} {
		res := postSettings(t, url, map[string]any{"bypass_subagents_enabled": enabled})
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("POST status = %d", res.StatusCode)
		}
		if applied.Permissions.BypassSubagents != enabled || decodeSettings(t, url)["bypass_subagents_enabled"] != enabled {
			t.Fatalf("setting failed to round-trip: want %v, config=%+v", enabled, applied.Permissions)
		}
		res = postSettings(t, url, map[string]any{"max_turns": 42})
		res.Body.Close()
		if res.StatusCode != http.StatusOK || applied.Permissions.BypassSubagents != enabled {
			t.Fatalf("unrelated save reset setting: %+v", applied.Permissions)
		}
	}
}
