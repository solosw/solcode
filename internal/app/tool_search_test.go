package app

import (
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/tool"
)

func TestToolSearchStartupAndReload(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Session.Dir = t.TempDir()
	cfg.Memory.Dir = t.TempDir()
	cfg.Memory.Enabled, cfg.Session.Enabled, cfg.KnowledgeGraph.Enabled = false, false, false
	cfg.Memory.Organizer.Enabled = false
	off := false
	cfg.CodeGraph.Enabled = &off
	application, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	for i, enabled := range []bool{true, false, true} {
		if i > 0 {
			cfg.ToolSearch.Enabled = &enabled
			if err := application.ReloadFeatures(cfg, nil); err != nil {
				t.Fatal(err)
			}
		}
		if (application.Tools.Find(tool.ToolSearchToolName) != nil) != enabled {
			t.Fatalf("registration enabled=%v", enabled)
		}
		ec := engineConfig(cfg, nil, nil, application.Tools, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		if ec.DisableToolSearch == enabled {
			t.Fatal("engine setting not wired")
		}
		names := map[string]bool{}
		for _, entry := range engine.SelectToolsForTurn(application.Tools.All(), nil, "", nil, application.Config.ToolSearchEnabled()) {
			names[entry.Name()] = true
		}
		if names[tool.WebSearchToolName] == enabled || names[tool.ToolSearchToolName] != enabled {
			t.Fatalf("wrong tools for enabled=%v: %+v", enabled, names)
		}
	}
}
