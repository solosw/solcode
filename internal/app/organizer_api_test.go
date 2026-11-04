package app

import (
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/organizer"
)

func TestOrganizerRuntimeSelectsAPI(t *testing.T) {
	cfg := config.Default()
	cfg.Memory.Organizer = config.OrganizerConfig{Enabled: true, Runtime: "api", Model: "summary", BaseURL: "https://organizer.example.test/v1", APIKey: "key"}
	cfg.NormalizeLocalModels()
	runtime := newOrganizerRuntime(cfg)
	if runtime == nil || !runtime.Available() {
		t.Fatal("API organizer not initialized")
	}
	defer runtime.Close()
	if _, ok := runtime.org.Generator().(*organizer.APIGenerator); !ok {
		t.Fatal("API config selected local inference")
	}
	cfg.Memory.Organizer.APIKey = ""
	if newOrganizerRuntime(cfg) != nil {
		t.Fatal("incomplete API configuration must not fall back to local or chat")
	}
}

func TestEngineConfigReceivesUserSubagentModel(t *testing.T) {
	cfg := config.Default()
	cfg.Subagents.Model = "child-model"
	ec := engineConfig(cfg, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil)
	if ec.SubagentModelName != "child-model" {
		t.Fatalf("engine subagent model = %q", ec.SubagentModelName)
	}
}
