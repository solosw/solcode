package app

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/sessionmemory"
	"github.com/solosw/solcode/internal/tool"
)

func TestUnifiedWriteStartupAndReload(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Session.Dir = t.TempDir()
	cfg.Memory.Dir = filepath.Join(t.TempDir(), "memory")
	cfg.Memory.Enabled, cfg.Session.Enabled, cfg.KnowledgeGraph.Enabled = false, false, false
	cfg.Memory.Organizer.Enabled = false
	disabled := false
	cfg.CodeGraph.Enabled = &disabled
	application, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	var schema string
	for i, enabled := range []bool{false, true, false} {
		if i > 0 {
			cfg.Memory.Enabled = enabled
			if err := application.ReloadFeatures(cfg, nil); err != nil {
				t.Fatal(err)
			}
		}
		entry := application.Tools.Find(tool.WriteMemoryToolName)
		if entry == nil {
			t.Fatal("unified write missing", enabled)
		}
		raw, _ := json.Marshal(entry.InputSchema())
		if schema != "" && schema != string(raw) {
			t.Fatal("memory toggle changed shared write schema")
		}
		schema = string(raw)
		selected := engine.SelectToolsForTurn(application.Tools.All(), nil, "WriteSessionMemory", map[string]bool{tool.WriteSessionMemoryToolName: true})
		found := false
		for _, candidate := range selected {
			if candidate.Name() == tool.WriteSessionMemoryToolName {
				t.Fatal("legacy writer exposed")
			}
			if candidate.Name() == tool.WriteMemoryToolName {
				found = true
			}
		}
		if !found {
			t.Fatal("unified writer absent from core")
		}
		uctx := &tool.UseContext{SessionID: "s1", WorkDir: cfg.WorkDir}
		result, err := entry.Invoke(t.Context(), uctx, json.RawMessage(`{"source":"session_memory","memory":"Problem: combine memory tools. Status: implemented and verified.","tags":["unified-memory"]}`))
		if err != nil || result.IsError {
			t.Fatalf("session write enabled=%v: %+v %v", enabled, result, err)
		}
		log, err := sessionmemory.NewStore(cfg.WorkDir).ReadForSession(t.Context(), "s1", "", 10)
		if err != nil || len(log) == 0 {
			t.Fatalf("session log not persisted: %v", err)
		}
		result, err = entry.Invoke(t.Context(), uctx, json.RawMessage(`{"memory":"Build Solcode with go build ./cmd/solcode.","kind":"workflow"}`))
		if err != nil || result.IsError == enabled {
			t.Fatalf("archival write enabled=%v: %+v %v", enabled, result, err)
		}
		if enabled {
			items, err := application.MemoryStore.List(t.Context())
			if err != nil || len(items) != 1 || items[0].SourceSessionID != "s1" || items[0].Text != "Build Solcode with go build ./cmd/solcode." {
				t.Fatalf("archival write not persisted: %+v %v", items, err)
			}
		}
	}
	cfg.Memory.Enabled = true
	cfg.Memory.Organizer = config.OrganizerConfig{Enabled: true, Runtime: "api", Model: "summary", APIKey: "test-key", BaseURL: "http://127.0.0.1:1/v1"}
	if err := application.ReloadFeatures(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if application.Tools.Find(tool.WriteMemoryToolName) != nil || application.Tools.Find(tool.WriteSessionMemoryToolName) != nil {
		t.Fatal("organizer exposed manual writers")
	}
	cfg.Memory.Organizer.Enabled = false
	if err := application.ReloadFeatures(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if application.Tools.Find(tool.WriteMemoryToolName) == nil {
		t.Fatal("writer not restored when organizer disabled")
	}
}
