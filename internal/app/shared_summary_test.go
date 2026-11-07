package app

import (
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
	"strings"
	"testing"
)

func TestUnconfiguredMainModelDoesNotDiscardHistory(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "chat"
	cfg.FastModel = "fast"
	app := &App{Config: cfg}
	current := compressionTestSession(t)
	before := session.Transcript(current.Messages)
	if _, err := app.compactSession(t.Context(), current, true); err == nil || !strings.Contains(err.Error(), "main model") {
		t.Fatalf("missing shared backend accepted: %v", err)
	}
	if session.Transcript(current.Messages) != before {
		t.Fatal("history lost without a summary backend")
	}
}
func TestExplicitCompactArchiveSurvivesSessionReload(t *testing.T) {
	app, _ := newCompressionTestApp(t, "Finished task; more work remains.", nil)
	current := compressionTestSession(t)
	if _, err := app.compactSession(t.Context(), current, true); err != nil {
		t.Fatal(err)
	}
	store := session.NewFileStore(t.TempDir())
	if err := store.Save(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Compression == nil {
		t.Fatal("missing archive state")
	}
	page, err := loaded.Compression.Read("m2", false, 0, 4000)
	if err != nil || !strings.Contains(page["text"].(string), "EXACT_TOOL_EVIDENCE") {
		t.Fatal("original evidence lost", err)
	}
}
