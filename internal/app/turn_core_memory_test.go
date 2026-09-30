package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/memory"
	"github.com/solosw/solcode/internal/session"
)

func TestRetrieveTurnCoreMemoryContextInjectsCoreAndBeliefs(t *testing.T) {
	work := t.TempDir()
	memDir := filepath.Join(work, "memories")
	store := memory.NewFileStore(memDir)
	mgr := memory.NewManager(store, nil, nil)

	pref := memory.NewItem("User prefers concise Chinese replies in this project", memory.TierLongTerm, "main")
	pref.Kind = memory.KindPreference
	pref.Scope = memory.ScopeProject
	pref.Confidence = 0.9
	if _, err := store.Save(context.Background(), pref); err != nil {
		t.Fatal(err)
	}
	rule := memory.NewItem("Never commit generated files under internal/generated", memory.TierLongTerm, "main")
	rule.Kind = memory.KindConstraint
	rule.Scope = memory.ScopeProject
	rule.Confidence = 0.95
	if _, err := store.Save(context.Background(), rule); err != nil {
		t.Fatal(err)
	}

	application := &App{
		Config: config.Config{
			WorkDir: work,
			Memory:  config.MemoryConfig{Enabled: true, Dir: memDir},
			Session: config.SessionConfig{Dir: t.TempDir(), DefaultSession: "main"},
		},
		MemoryManager: mgr,
		MemoryStore:   store,
	}
	current := &session.Session{}
	current.Metadata.ID = session.SessionID("main")
	current.Metadata.WorkDir = work
	// Cross-session off, not a new session → turn-level lightweight path.
	cross := false
	current.Metadata.CrossSessionMemory = &cross

	items, err := application.retrieveNewSessionMemoryContext(context.Background(), "hello", current, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected turn-level core/beliefs injection")
	}
	var sawCore, sawBeliefs bool
	var blob strings.Builder
	for _, item := range items {
		if item.Source == "core" {
			sawCore = true
		}
		if item.Source == "beliefs" {
			sawBeliefs = true
		}
		blob.WriteString(item.Content)
		blob.WriteByte('\n')
	}
	if !sawCore && !sawBeliefs {
		t.Fatalf("items = %#v, want core and/or beliefs", items)
	}
	text := blob.String()
	if !strings.Contains(text, "concise Chinese") && !strings.Contains(text, "generated") {
		t.Fatalf("injection missing remembered content: %q", text)
	}
}

func TestRetrieveTurnCoreMemoryContextEmptyStore(t *testing.T) {
	work := t.TempDir()
	store := memory.NewFileStore(filepath.Join(work, "memories"))
	application := &App{
		Config: config.Config{
			WorkDir: work,
			Memory:  config.MemoryConfig{Enabled: true},
		},
		MemoryManager: memory.NewManager(store, nil, nil),
		MemoryStore:   store,
	}
	current := &session.Session{}
	current.Metadata.ID = "empty"
	items, err := application.retrieveTurnCoreMemoryContext(context.Background(), current, "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %#v", items)
	}
}
