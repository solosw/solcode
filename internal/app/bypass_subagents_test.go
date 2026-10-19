package app

import (
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestBypassSubagentsStartupAndReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, initial := range []bool{false, true} {
		t.Run(map[bool]string{false: "default off", true: "enabled"}[initial], func(t *testing.T) {
			cfg := config.Default()
			cfg.WorkDir = t.TempDir()
			cfg.Memory.Enabled, cfg.Session.Enabled, cfg.KnowledgeGraph.Enabled = false, false, false
			cfg.PermissionMode, cfg.Permissions.Mode = permission.ModeBypass, permission.ModeBypass
			cfg.Permissions.BypassSubagents = initial
			application, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer application.Close()
			if application.Permissions.DelegationEnabled() != initial {
				t.Fatalf("startup ignored setting: want %v", initial)
			}
			permissions := application.Permissions
			permissions.DenyTool(tool.WriteToolName)
			for _, reload := range []func(config.Config) error{
				application.SwitchModel,
				func(next config.Config) error { return application.ReloadFeatures(next, nil) },
			} {
				for _, enabled := range []bool{true, false} {
					cfg.Permissions.BypassSubagents = enabled
					if err := reload(cfg); err != nil {
						t.Fatal(err)
					}
					if application.Permissions != permissions || permissions.Mode() != permission.ModeBypass {
						t.Fatal("reload replaced session permissions or mode")
					}
					if permissions.DelegationEnabled() != enabled || application.Config.Permissions.BypassSubagents != enabled {
						t.Fatalf("reload ignored setting: want %v", enabled)
					}
					if permissions.Check(tool.NewWriteTool(), []byte(`{"path":"x","content":"x"}`)).Allowed {
						t.Fatal("reload lost session denials")
					}
				}
			}
		})
	}
}
