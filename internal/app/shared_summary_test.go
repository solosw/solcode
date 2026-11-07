package app

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
	"strings"
	"testing"
)

func TestUnconfiguredOrganizerDoesNotDiscardHistoryOrUseChatModel(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "chat"
	cfg.FastModel = "fast"
	app := &App{Config: cfg}
	current := session.NewSession("main", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("user request")), sdk.NewAssistantMessage(sdk.NewTextBlock("completed work")))
	before := session.Transcript(current.Messages)
	if _, err := app.compactSession(t.Context(), current, true); err == nil || !strings.Contains(err.Error(), "memory.organizer") {
		t.Fatalf("missing shared backend accepted: %v", err)
	}
	if session.Transcript(current.Messages) != before {
		t.Fatal("history lost without a summary backend")
	}
}
func TestExplicitCompactArchiveSurvivesSessionReload(t *testing.T) {
	cfg := config.Default()
	app := &App{Config: cfg, summaryWriter: &recordingSummaryWriter{summary: "Finished task; more work remains."}}
	current := session.NewSession("main", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("ORIGINAL_REQUEST")), sdk.NewAssistantMessage(sdk.NewTextBlock("EXACT_TOOL_EVIDENCE")))
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
