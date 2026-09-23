package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/memory"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func newMemoryWriterApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Default()
	cfg.Memory.Enabled = true
	cfg.Memory.Dir = filepath.Join(t.TempDir(), "memory")
	store := memory.NewFileStore(cfg.Memory.Dir)
	return &App{
		Config:        cfg,
		MemoryStore:   store,
		MemoryManager: memory.NewManager(store, memory.DefaultGate{}, memory.StaticJudge{}),
	}
}

func TestAppWriteMemoryPersistsEntry(t *testing.T) {
	application := newMemoryWriterApp(t)

	result, err := application.WriteMemory(context.Background(), tool.MemoryWriteRequest{
		Text:      "Solcode tests live under unit_tests and internal package dirs.",
		Kind:      "fact",
		Scope:     "project",
		Tags:      []string{"testing"},
		SessionID: "s1",
	})
	if err != nil {
		t.Fatalf("WriteMemory() = %v", err)
	}
	if !result.Stored || result.Merged {
		t.Fatalf("result = %#v, want a newly stored entry", result)
	}
	if result.Tier != string(memory.TierLongTerm) {
		t.Fatalf("tier = %q, want %q for a fact", result.Tier, memory.TierLongTerm)
	}

	items, err := application.MemoryStore.List(context.Background())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("stored items = %d, want 1", len(items))
	}
	if items[0].SourceSessionID != "s1" {
		t.Fatalf("source session = %q, want s1", items[0].SourceSessionID)
	}
	if items[0].Kind != memory.KindFact || items[0].Scope != memory.ScopeProject {
		t.Fatalf("kind/scope = %q/%q, want fact/project", items[0].Kind, items[0].Scope)
	}
}

func TestAppWriteMemoryRecordsCheckpointTurnAndAllowsDuplicates(t *testing.T) {
	application := newMemoryWriterApp(t)
	application.Config.WorkDir = t.TempDir()
	application.Config.Session.Dir = t.TempDir()
	application.Config.Session.DefaultSession = "main"
	application.beginCheckpointTurn("main", application.Config.WorkDir, "remember turn")

	req := tool.MemoryWriteRequest{
		Text:      "Build the CLI with go build ./cmd/solcode before manual checks.",
		Kind:      "workflow",
		SessionID: "main",
		WorkDir:   application.Config.WorkDir,
	}
	first, err := application.WriteMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("first WriteMemory() = %v", err)
	}
	if !first.Stored || first.Merged {
		t.Fatalf("first = %#v, want a newly stored entry", first)
	}
	if first.Turn < 0 {
		t.Fatalf("turn = %d, want a real checkpoint turn", first.Turn)
	}

	second, err := application.WriteMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("second WriteMemory() = %v", err)
	}
	if !second.Stored || second.Merged {
		t.Fatalf("second = %#v, want a duplicate stored entry", second)
	}
	if second.Turn != first.Turn {
		t.Fatalf("second turn = %d, want %d", second.Turn, first.Turn)
	}
	if second.ID == first.ID {
		t.Fatalf("duplicate write reused id %q", second.ID)
	}

	items, err := application.MemoryStore.List(context.Background())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("stored items = %d, want 2 duplicates", len(items))
	}
	for _, item := range items {
		if item.SourceTurn != first.Turn {
			t.Fatalf("item turn = %d, want %d", item.SourceTurn, first.Turn)
		}
	}
}

func TestAppWriteMemoryTierFollowsKind(t *testing.T) {
	cases := []struct {
		kind string
		want memory.Tier
	}{
		{"fact", memory.TierLongTerm},
		{"preference", memory.TierLongTerm},
		{"constraint", memory.TierLongTerm},
		{"workflow", memory.TierProcedural},
		{"task", memory.TierShortTerm},
	}
	for _, tc := range cases {
		if got := memoryTierForWrite(tc.kind); got != tc.want {
			t.Fatalf("memoryTierForWrite(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestAppWriteMemoryRejectsSensitiveContent(t *testing.T) {
	application := newMemoryWriterApp(t)

	result, err := application.WriteMemory(context.Background(), tool.MemoryWriteRequest{
		Text:      "The deploy API key is sk-ant-api03-secretvalue-do-not-store",
		SessionID: "s1",
	})
	if err != nil {
		t.Fatalf("WriteMemory() = %v", err)
	}
	if result.Stored {
		t.Fatalf("result = %#v, want the gate to reject secret-looking content", result)
	}

	items, err := application.MemoryStore.List(context.Background())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("stored items = %d, want nothing persisted", len(items))
	}
}

func TestAppWriteMemoryAllowsNearDuplicate(t *testing.T) {
	application := newMemoryWriterApp(t)
	req := tool.MemoryWriteRequest{
		Text:      "Build the CLI with go build ./cmd/solcode before manual checks.",
		Kind:      "workflow",
		SessionID: "s1",
	}
	if _, err := application.WriteMemory(context.Background(), req); err != nil {
		t.Fatalf("first WriteMemory() = %v", err)
	}

	req.Text = "Build the CLI with go build ./cmd/solcode before manual verification checks."
	result, err := application.WriteMemory(context.Background(), req)
	if err != nil {
		t.Fatalf("second WriteMemory() = %v", err)
	}
	if result.Merged || !result.Stored {
		t.Fatalf("result = %#v, want a second stored entry without merging", result)
	}

	items, err := application.MemoryStore.List(context.Background())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("stored items = %d, want both writes kept", len(items))
	}
}

// seedMemory writes one entry attributed to the given session id.
func seedMemory(t *testing.T, application *App, sessionID, text, kind string) {
	t.Helper()
	if _, err := application.WriteMemory(context.Background(), tool.MemoryWriteRequest{
		Text:      text,
		Kind:      kind,
		SessionID: sessionID,
	}); err != nil {
		t.Fatalf("seed WriteMemory(%q) = %v", text, err)
	}
}

func TestAppReadMemoryReturnsOwnEntries(t *testing.T) {
	application := newMemoryWriterApp(t)
	application.Sessions = session.NewManager(session.NewFileStore(filepath.Join(t.TempDir(), "sessions")), "main")
	seedMemory(t, application, "s1", "Run go build ./cmd/solcode to compile the CLI.", "workflow")

	result, err := application.ReadMemory(context.Background(), tool.MemoryReadRequest{
		Query:     "build the cli",
		Limit:     5,
		SessionID: "s1",
	})
	if err != nil {
		t.Fatalf("ReadMemory() = %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %#v, want the session's own memory", result.Entries)
	}
	if result.Entries[0].OtherSession {
		t.Fatal("own entry must not be flagged as another session's")
	}
	if result.Entries[0].Tier != string(memory.TierProcedural) {
		t.Fatalf("tier = %q, want %q for a workflow", result.Entries[0].Tier, memory.TierProcedural)
	}
}

func TestAppReadMemoryHidesOtherSessionsWhenOptedOut(t *testing.T) {
	application := newMemoryWriterApp(t)
	sessions := session.NewManager(session.NewFileStore(filepath.Join(t.TempDir(), "sessions")), "main")
	application.Sessions = sessions

	seedMemory(t, application, "s1", "Session one records the linting rule for imports.", "fact")
	seedMemory(t, application, "other", "Another session recorded a deployment checklist step.", "fact")

	// s1 declined cross-session memory.
	current, err := sessions.LoadOrCreate(context.Background(), "s1", application.Config.WorkDir, application.Config.Model)
	if err != nil {
		t.Fatalf("LoadOrCreate() = %v", err)
	}
	denied := false
	current.Metadata.CrossSessionMemory = &denied
	if err := sessions.Save(context.Background(), current); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	result, err := application.ReadMemory(context.Background(), tool.MemoryReadRequest{Limit: 10, SessionID: "s1"})
	if err != nil {
		t.Fatalf("ReadMemory() = %v", err)
	}
	if result.CrossSessionAllowed {
		t.Fatal("CrossSessionAllowed should be false for an opted-out session")
	}
	for _, entry := range result.Entries {
		if entry.OtherSession {
			t.Fatalf("opted-out session must not see other sessions' memory: %#v", entry)
		}
	}

	// Opting in exposes the other session's entry.
	allowed := true
	current.Metadata.CrossSessionMemory = &allowed
	if err := sessions.Save(context.Background(), current); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	result, err = application.ReadMemory(context.Background(), tool.MemoryReadRequest{Limit: 10, SessionID: "s1"})
	if err != nil {
		t.Fatalf("ReadMemory() after opt-in = %v", err)
	}
	if !result.CrossSessionAllowed {
		t.Fatal("CrossSessionAllowed should be true after opting in")
	}
	sawOther := false
	for _, entry := range result.Entries {
		if entry.OtherSession {
			sawOther = true
		}
	}
	if !sawOther {
		t.Fatalf("entries = %#v, want an entry from the other session", result.Entries)
	}
}

func TestAppReadMemoryFiltersByKind(t *testing.T) {
	application := newMemoryWriterApp(t)
	application.Sessions = session.NewManager(session.NewFileStore(filepath.Join(t.TempDir(), "sessions")), "main")
	seedMemory(t, application, "s1", "The reviewer prefers short commit subjects.", "preference")

	result, err := application.ReadMemory(context.Background(), tool.MemoryReadRequest{
		Kind:      "workflow",
		Limit:     5,
		SessionID: "s1",
	})
	if err != nil {
		t.Fatalf("ReadMemory() = %v", err)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("entries = %#v, want nothing for a non-matching kind", result.Entries)
	}
	if result.Note == "" {
		t.Fatal("expected a note explaining the filter removed all entries")
	}

	result, err = application.ReadMemory(context.Background(), tool.MemoryReadRequest{
		Kind:      "preference",
		Limit:     5,
		SessionID: "s1",
	})
	if err != nil {
		t.Fatalf("ReadMemory() = %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries = %#v, want the matching preference", result.Entries)
	}
}

func TestAppReadMemoryDisabledReturnsError(t *testing.T) {
	cfg := config.Default()
	cfg.Memory.Enabled = false
	application := &App{Config: cfg}
	if _, err := application.ReadMemory(context.Background(), tool.MemoryReadRequest{Limit: 5}); err == nil {
		t.Fatal("expected an error when memory is disabled")
	}
}

func TestAppWriteMemoryDisabledReturnsError(t *testing.T) {
	cfg := config.Default()
	cfg.Memory.Enabled = false
	application := &App{Config: cfg}
	if _, err := application.WriteMemory(context.Background(), tool.MemoryWriteRequest{Text: "x"}); err == nil {
		t.Fatal("expected an error when memory is disabled")
	}
}
