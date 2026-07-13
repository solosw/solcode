package changegraph

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesLegacyEventSymbolSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = legacy.Exec(`
CREATE TABLE graph_files (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, language TEXT, updated_at INTEGER NOT NULL);
CREATE TABLE graph_symbols (id INTEGER PRIMARY KEY, file_id INTEGER NOT NULL, symbol_key TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, name TEXT NOT NULL, full_name TEXT, start_line INTEGER, end_line INTEGER, active INTEGER NOT NULL DEFAULT 1, first_seen_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL);
CREATE TABLE change_events (id INTEGER PRIMARY KEY, session_id TEXT, tool_name TEXT NOT NULL, file_id INTEGER NOT NULL, description TEXT NOT NULL, changed_lines TEXT, occurred_at INTEGER NOT NULL);
CREATE TABLE change_event_symbols (event_id INTEGER NOT NULL, symbol_id INTEGER NOT NULL, PRIMARY KEY(event_id, symbol_id));
INSERT INTO graph_files(id, path, updated_at) VALUES(1, 'legacy.go', 1);
INSERT INTO graph_symbols(id, file_id, symbol_key, kind, name, start_line, end_line, first_seen_at, last_seen_at) VALUES(1, 1, 'legacy.go:function:legacy', 'function', 'legacy', 2, 4, 1, 1);
INSERT INTO change_events(id, session_id, tool_name, file_id, description, occurred_at) VALUES(1, 'main', 'Edit', 1, 'legacy change', 1);
INSERT INTO change_event_symbols(event_id, symbol_id) VALUES(1, 1);
`)
	if err != nil {
		_ = legacy.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = store.Close() }()
	var changeKindColumn int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('change_event_symbols') WHERE name='change_kind'`).Scan(&changeKindColumn); err != nil {
		t.Fatalf("inspect migrated schema: %v", err)
	}
	if changeKindColumn != 1 {
		t.Fatalf("change_kind columns = %d, want 1", changeKindColumn)
	}
	events, err := store.Recent(context.Background(), "main", 1)
	if err != nil {
		t.Fatalf("read migrated event: %v", err)
	}
	if len(events) != 1 || len(events[0].Symbols) != 1 || events[0].Symbols[0].ChangeKind != SymbolModified {
		t.Fatalf("migrated events = %#v", events)
	}
}

func TestStoreRecordsTimestampedChanges(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = store.Close() }()
	occurredAt := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	if err := store.Record(context.Background(), Change{
		SessionID: "main", ToolName: "Edit", Path: "internal/app/app.go",
		Description: "record changes", OccurredAt: occurredAt,
		Symbols: []Symbol{{Kind: "function", Name: "Run", StartLine: 10, EndLine: 25}},
	}); err != nil {
		t.Fatalf("Record() = %v", err)
	}
	events, err := store.Recent(context.Background(), "main", 10)
	if err != nil {
		t.Fatalf("Recent() = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Path != "internal/app/app.go" || events[0].Description != "record changes" {
		t.Fatalf("event = %#v", events[0])
	}
	if !events[0].OccurredAt.Equal(occurredAt) {
		t.Fatalf("OccurredAt = %s, want %s", events[0].OccurredAt, occurredAt)
	}
	if events[0].ToolName != "Edit" {
		t.Fatalf("ToolName = %q, want Edit", events[0].ToolName)
	}
}

func TestStorePrunesOldestEventsAtConfiguredLimit(t *testing.T) {
	store, err := OpenWithOptions(filepath.Join(t.TempDir(), "knowledge.db"), Options{MaxEvents: 2})
	if err != nil {
		t.Fatalf("OpenWithOptions() = %v", err)
	}
	defer func() { _ = store.Close() }()

	for i, description := range []string{"first", "second", "third"} {
		if err := store.Record(context.Background(), Change{
			SessionID: "main", ToolName: "Write", Path: "file.go", Description: description,
			OccurredAt: time.Date(2026, 3, 20, 12, 0, i, 0, time.UTC),
		}); err != nil {
			t.Fatalf("Record(%q) = %v", description, err)
		}
	}
	events, err := store.Recent(context.Background(), "main", 10)
	if err != nil {
		t.Fatalf("Recent() = %v", err)
	}
	if len(events) != 2 || events[0].Description != "third" || events[1].Description != "second" {
		t.Fatalf("events = %#v", events)
	}
}

func TestBuildContextIncludesRecentProjectChangesFromOtherSessions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = store.Close() }()
	for _, change := range []Change{
		{SessionID: "main", ToolName: "Edit", Path: "main.go", Description: "update main", OccurredAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)},
		{SessionID: "review", ToolName: "Edit", Path: "review.go", Description: "update review", OccurredAt: time.Date(2026, 3, 20, 12, 1, 0, 0, time.UTC)},
	} {
		if err := store.Record(context.Background(), change); err != nil {
			t.Fatalf("Record(%q) = %v", change.Description, err)
		}
	}
	contextText, err := store.BuildContext(context.Background(), "main", 4_000)
	if err != nil {
		t.Fatalf("BuildContext() = %v", err)
	}
	for _, want := range []string{"main.go", "update main", "review.go", "update review"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("BuildContext() = %q, missing %q", contextText, want)
		}
	}
}

func TestBuildRelevantContextPrioritizesPromptMatchedCrossSessionChange(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = store.Close() }()
	for _, change := range []Change{
		{
			SessionID: "main", ToolName: "Edit", Path: "ui.go", Description: "adjust color", OccurredAt: time.Date(2026, 3, 20, 12, 2, 0, 0, time.UTC),
			Symbols: []Symbol{{Kind: "function", Name: "renderTheme", ChangeKind: SymbolModified}},
		},
		{
			SessionID: "review", ToolName: "Edit", Path: "auth.go", Description: "fix token validation", OccurredAt: time.Date(2026, 3, 20, 12, 1, 0, 0, time.UTC),
			Symbols: []Symbol{{Kind: "function", Name: "validateToken", ChangeKind: SymbolModified}},
		},
	} {
		if err := store.Record(context.Background(), change); err != nil {
			t.Fatalf("Record(%q) = %v", change.Description, err)
		}
	}
	contextText, err := store.BuildRelevantContext(context.Background(), "main", "please update validateToken authentication", 4_000)
	if err != nil {
		t.Fatalf("BuildRelevantContext() = %v", err)
	}
	targeted := strings.Index(contextText, "auth.go")
	generic := strings.Index(contextText, "ui.go")
	if targeted < 0 || generic < 0 || targeted > generic {
		t.Fatalf("matched cross-session event should rank first: %q", contextText)
	}
	if !strings.Contains(contextText, "session review") {
		t.Fatalf("expected cross-session label: %q", contextText)
	}
}

func TestBuildContextHonorsCharacterBudget(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Record(context.Background(), Change{
		SessionID: "main", ToolName: "Write", Path: "长文件名.go", Description: "更新描述用于预算测试",
	}); err != nil {
		t.Fatalf("Record() = %v", err)
	}
	const budget = 32
	contextText, err := store.BuildContext(context.Background(), "main", budget)
	if err != nil {
		t.Fatalf("BuildContext() = %v", err)
	}
	if got := len([]rune(contextText)); got > budget {
		t.Fatalf("context length = %d, want <= %d: %q", got, budget, contextText)
	}
}

func TestRecordFileChangeClassifiesAddedModifiedAndDeletedSymbols(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = store.Close() }()

	before := `package demo

func keep() string { return "old" }
func remove() {}
`
	after := `package demo

func keep() string { return "new" }
func added() {}
`
	if err := store.RecordFileChange(context.Background(), FileChange{
		SessionID: "main", ToolName: "Edit", Path: "demo.go", Description: "refactor functions", Before: before, After: after,
	}); err != nil {
		t.Fatalf("RecordFileChange() = %v", err)
	}
	events, err := store.Recent(context.Background(), "main", 1)
	if err != nil {
		t.Fatalf("Recent() = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	changes := map[string]SymbolChangeKind{}
	for _, symbol := range events[0].Symbols {
		changes[symbol.Name] = symbol.ChangeKind
	}
	for name, want := range map[string]SymbolChangeKind{
		"keep":   SymbolModified,
		"added":  SymbolAdded,
		"remove": SymbolDeleted,
	} {
		if got := changes[name]; got != want {
			t.Fatalf("symbol %q change = %q, want %q; symbols = %#v", name, got, want, events[0].Symbols)
		}
	}

	contextText, err := store.BuildContext(context.Background(), "main", 4_000)
	if err != nil {
		t.Fatalf("BuildContext() = %v", err)
	}
	for _, want := range []string{"modified function keep", "added function added", "deleted function remove"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("BuildContext() = %q, missing %q", contextText, want)
		}
	}
}

func TestRecordFileChangeBuildsSymbolContext(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = store.Close() }()

	before := "package demo\n\nfunc greet() string { return \"old\" }\n"
	after := "package demo\n\nfunc greet() string { return \"new\" }\n"
	if err := store.RecordFileChange(context.Background(), FileChange{
		SessionID: "main", ToolName: "Edit", Path: "demo.go", Description: "update greeting", Before: before, After: after,
	}); err != nil {
		t.Fatalf("RecordFileChange() = %v", err)
	}

	events, err := store.Recent(context.Background(), "main", 1)
	if err != nil {
		t.Fatalf("Recent() = %v", err)
	}
	if len(events) != 1 || events[0].Language != "go" || events[0].ChangedLines == "" {
		t.Fatalf("event metadata = %#v", events)
	}
	contextText, err := store.BuildContext(context.Background(), "main", 4_000)
	if err != nil {
		t.Fatalf("BuildContext() = %v", err)
	}
	for _, want := range []string{"demo.go", "update greeting", "function greet", "Edit", "go", "lines "} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("BuildContext() = %q, missing %q", contextText, want)
		}
	}
}

func TestRecordFileChangeRequiresShortDescription(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.RecordFileChange(context.Background(), FileChange{Path: "a.go", Description: strings.Repeat("汉", MaxDescriptionRunes+1), Before: "a", After: "b"}); err == nil {
		t.Fatal("expected description exceeding 30 Chinese characters to fail")
	}
}
