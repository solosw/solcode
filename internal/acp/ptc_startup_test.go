package acp

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

func TestACPStartupWithPTCEnabledAndDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for iteration := range 5 {
			t.Run(fmt.Sprintf("ptc=%t/start=%d", enabled, iteration), func(t *testing.T) {
				client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
					cfg.Batch.PTC.Enabled = enabled
					return cfg
				}, nil)
				mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
				var created SessionNewResult
				if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &created); err != nil {
					t.Fatal(err)
				}
				if created.SessionID == "" || created.Modes == nil {
					t.Fatalf("incomplete startup response: %+v", created)
				}
				sess := client.server.getSession(created.SessionID)
				if sess == nil || sess.application == nil || sess.cfg.Batch.PTC.Enabled != enabled {
					t.Fatal("ACP startup lost PTC settings")
				}
				batch := sess.application.Tools.Find(tool.BatchToolName)
				if batch == nil {
					t.Fatal("BatchTool was not registered")
				}
				properties := batch.InputSchema()["properties"].(map[string]any)
				_, hasCode := properties["code"]
				_, hasOperations := properties["operations"]
				if hasCode != enabled || hasOperations == enabled {
					t.Fatalf("PTC enabled=%v, schema code=%v operations=%v", enabled, hasCode, hasOperations)
				}
				// Metadata can be read without starting a JS runtime or tool call.
				if batch.Description() == "" {
					t.Fatal("BatchTool metadata is unavailable after startup")
				}
				waitFor(t, func() bool { return hasKind(client.snapshot(), "available_commands_update") })
			})
		}
	}
}
