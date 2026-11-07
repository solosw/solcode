package app

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func TestUnifiedSearchStartupAndReload(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.Memory.Enabled, cfg.Session.Enabled, cfg.KnowledgeGraph.Enabled = false, false, false
	cfg.Memory.Dir = filepath.Join(t.TempDir(), "memory")
	cfg.Memory.Organizer.Enabled = false
	disabled := false
	cfg.CodeGraph.Enabled = &disabled
	application, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	var schema string
	for _, enabled := range []bool{false, true, false} {
		if enabled != cfg.Memory.Enabled {
			cfg.Memory.Enabled = enabled
			if err := application.ReloadFeatures(cfg, nil); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{tool.CompressToolName, tool.DecompressToolName, tool.SearchContextToolName, tool.ACPStatusToolName} {
			count := 0
			for _, candidate := range application.Tools.All() {
				if candidate.Name() == name {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("%s registered %d times after enabled=%v", name, count, enabled)
			}
		}
		selected := engine.SelectToolsForTurn(application.Tools.All(), nil, "ReadMemory ReadSessionMemory", nil)
		for _, candidate := range selected {
			if candidate.Name() == tool.ReadMemoryToolName || candidate.Name() == tool.ReadSessionMemoryToolName {
				t.Fatalf("legacy read exposed: %s", candidate.Name())
			}
		}
		search := application.Tools.Find(tool.SearchContextToolName)
		encoded, _ := json.Marshal(search.InputSchema())
		if schema != "" && schema != string(encoded) {
			t.Fatal("memory toggle changed shared search schema")
		}
		schema = string(encoded)
		result, err := search.Invoke(t.Context(), &tool.UseContext{SessionID: "s1", WorkDir: cfg.WorkDir}, json.RawMessage(`{"source":"memory"}`))
		if err != nil || result.IsError {
			t.Fatalf("search = %+v %v", result, err)
		}
		var data struct {
			Sources map[string]struct {
				Status string `json:"status"`
			} `json:"sources"`
		}
		if err := json.Unmarshal([]byte(result.Text), &data); err != nil {
			t.Fatal(err)
		}
		want := "unavailable"
		if enabled {
			want = "ok"
		}
		if data.Sources["memory"].Status != want {
			t.Fatalf("memory enabled=%v result=%s", enabled, result.Text)
		}
	}
}

func TestUnifiedSearchHonorsMemoryConsent(t *testing.T) {
	application := newMemoryWriterApp(t)
	application.Config.WorkDir = t.TempDir()
	application.Sessions = session.NewManager(session.NewFileStore(t.TempDir()), "main")
	seedMemory(t, application, "s1", "OWN_BUILD_COMMAND use go build", "workflow")
	seedMemory(t, application, "other", "OTHER_BUILD_COMMAND use make build", "workflow")
	current, err := application.Sessions.LoadOrCreate(t.Context(), "s1", application.Config.WorkDir, application.Config.Model)
	if err != nil {
		t.Fatal(err)
	}
	search := tool.NewSearchContextTool(application, application)
	items, err := application.MemoryStore.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	otherTopic := ""
	for _, item := range items {
		if item.SourceSessionID == "other" {
			otherTopic = item.Topic
		}
	}
	if otherTopic == "" {
		t.Fatal("missing other-session topic fixture")
	}
	for _, allowed := range []bool{false, true} {
		current.Metadata.CrossSessionMemory = &allowed
		if err := application.Sessions.Save(t.Context(), current); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{"", "beliefs", "current beliefs", "topic:" + otherTopic} {
			params := map[string]string{"query": query}
			if query != "" {
				params["source"] = "memory"
			}
			input, _ := json.Marshal(params)
			result, err := search.Invoke(t.Context(), &tool.UseContext{SessionID: "s1", WorkDir: application.Config.WorkDir}, input)
			if err != nil || result.IsError {
				t.Fatalf("search = %+v %v", result, err)
			}
			if !strings.HasPrefix(query, "topic:") && !strings.Contains(result.Text, "OWN_BUILD_COMMAND") {
				t.Fatal("own memory lost:", result.Text)
			}
			if strings.Contains(result.Text, "OTHER_BUILD_COMMAND") != allowed {
				t.Fatalf("cross-session allowed=%v query=%q result=%s", allowed, query, result.Text)
			}
		}
	}
}
