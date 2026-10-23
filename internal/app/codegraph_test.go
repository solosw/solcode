package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

func TestCodeGraphDefaultRegistrationAndReload(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Memory.Enabled = false
	cfg.Session.Enabled = false
	cfg.KnowledgeGraph.Enabled = false
	// Deliberately missing runtime: lifecycle test must not start real processes.
	cfg.CodeGraph.RuntimeDir = t.TempDir()
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	old := app.Tools.Find(tool.CodeGraphToolName)
	if old == nil {
		t.Fatal("default CodeGraph tool not registered")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, err := old.Invoke(context.Background(), nil, json.RawMessage(`{"action":"status"}`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result.Text, "unavailable") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("App.New did not proactively start runtime:", result.Text)
		}
		time.Sleep(10 * time.Millisecond)
	}
	disabled := false
	cfg.CodeGraph.Enabled = &disabled
	if err := app.ReloadFeatures(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if app.Tools.Find(tool.CodeGraphToolName) != nil {
		t.Fatal("disabled tool still registered")
	}
	result, err := old.Invoke(context.Background(), nil, json.RawMessage(`{"action":"status"}`))
	if err != nil || !result.IsError || !strings.Contains(result.Text, "closed") {
		t.Fatalf("stale runtime survived reload: %+v %v", result, err)
	}
}
