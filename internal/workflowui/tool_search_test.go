package workflowui

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/workflow"
)

func TestToolSearchSettingsRoundTrip(t *testing.T) {
	stored := config.Default()
	stored.WorkDir = t.TempDir()
	path := filepath.Join(t.TempDir(), "settings.json")
	srv, url, err := Start(Config{
		WorkDir:  stored.WorkDir,
		List:     func() []workflow.Definition { return nil },
		Save:     func(workflow.Definition, workflow.SaveScope, *workflow.Layout) (string, error) { return "", nil },
		Settings: func() config.Config { return stored },
		ApplySettings: func(next config.Config) error {
			if err := config.SaveLocalOverrides(path, map[string]any{"tool_search": map[string]any{"enabled": next.ToolSearchEnabled()}}); err != nil {
				return err
			}
			loaded, err := config.Load(path)
			if err != nil {
				return err
			}
			stored = loaded
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if decodeSettings(t, url)["tool_search_enabled"] != true {
		t.Fatal("default not exposed")
	}
	for _, enabled := range []bool{false, true, false} {
		response := postSettings(t, url, map[string]any{"tool_search_enabled": enabled})
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", response.StatusCode)
		}
		if decodeSettings(t, url)["tool_search_enabled"] != enabled || stored.ToolSearchEnabled() != enabled {
			t.Fatal("toggle not persisted or returned")
		}
		response = postSettings(t, url, map[string]any{"max_turns": 2})
		response.Body.Close()
		if stored.ToolSearchEnabled() != enabled {
			t.Fatal("unrelated update reset toggle")
		}
	}
}
