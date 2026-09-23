package sessionmemory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendCreatesFileWithHeaderAndMetadata(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	wantPath := filepath.Join(dir, ".solcode", FileName)
	if store.Path() != wantPath {
		t.Fatalf("path = %q, want %q", store.Path(), wantPath)
	}

	entry, err := store.Append(context.Background(), Entry{
		Keywords:   []string{"Checkpoint", "rewind", ""},
		Summary:    "Added named checkpoints and Bash fingerprint capture.",
		Importance: 0.8,
		Turn:       4,
		Files:      []string{"internal/checkpoint/store.go"},
		Time:       time.Date(2026, 2, 3, 10, 0, 0, 0, time.Local),
		SessionID:  "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Keywords) != 2 || entry.Keywords[0] != "Checkpoint" {
		t.Fatalf("keywords = %#v", entry.Keywords)
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, Header) {
		t.Fatalf("missing header: %q", text)
	}
	for _, want := range []string{
		"turn 4", "importance 0.80", "session main",
		"keywords: Checkpoint, rewind",
		"files: internal/checkpoint/store.go",
		"Added named checkpoints and Bash fingerprint capture.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

func TestReadRecentAndFuzzySearch(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)

	entries := []Entry{
		{Keywords: []string{"build"}, Summary: "Build uses go build ./cmd/solcode.", Turn: 0, Time: base},
		{Keywords: []string{"checkpoint", "rewind"}, Summary: "Checkpoints restore code only.", Turn: 1, Time: base.Add(time.Hour)},
		{Keywords: []string{"mcp"}, Summary: "MCP transport unified to STREAMABLE_HTTP.", Turn: 2, Time: base.Add(2 * time.Hour)},
	}
	for _, e := range entries {
		if _, err := store.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	recent, err := store.Read(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].Turn != 2 || recent[1].Turn != 1 {
		t.Fatalf("recent = %#v", recent)
	}

	hits, err := store.Read(ctx, "rewind", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Turn != 1 {
		t.Fatalf("rewind hits = %#v", hits)
	}

	hits, err = store.Read(ctx, "STREAMABLE_HTTP", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Turn != 2 {
		t.Fatalf("mcp hits = %#v", hits)
	}
}

func TestPathUsesProjectSolcodeDir(t *testing.T) {
	dir := t.TempDir()
	if got := Path(dir); got != filepath.Join(dir, ".solcode", FileName) {
		t.Fatalf("Path = %q", got)
	}
	if got := Path(""); got != "" {
		t.Fatalf("empty workdir Path = %q, want empty", got)
	}
}

func TestListRoundTrips(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()
	if _, err := store.Append(ctx, Entry{Summary: "first", Turn: 1, Importance: 0.4}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, Entry{Summary: "second line\n\nmore", Turn: 2, Importance: 0.9}); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d", len(list))
	}
	if list[1].Turn != 2 || list[1].Importance != 0.9 {
		t.Fatalf("second = %#v", list[1])
	}
	if !strings.Contains(list[1].Summary, "more") {
		t.Fatalf("summary = %q", list[1].Summary)
	}
}

func TestAppendRequiresSummary(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Append(context.Background(), Entry{Summary: "   "}); err == nil {
		t.Fatal("expected error for empty summary")
	}
}

func TestReadMissingFileIsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	list, err := store.Read(context.Background(), "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %#v", list)
	}
}

func TestTodosRoundTripAndMerge(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.Local)

	if _, err := store.Append(ctx, Entry{
		Summary:   "Turn one snapshot.",
		Turn:      0,
		Time:      base,
		SessionID: "main",
		Todos: []TodoJudgment{
			{ID: "1", Content: "Wire turn end", Status: TodoInProgress, Valid: true},
			{ID: "2", Content: "Prune files", Status: TodoPending, Valid: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, Entry{
		Summary:   "Turn two snapshot.",
		Turn:      1,
		Time:      base.Add(time.Hour),
		SessionID: "main",
		Todos: []TodoJudgment{
			{ID: "1", Content: "Wire turn end", Status: TodoCompleted, Valid: true, Done: true},
			{ID: "2", Content: "Prune files", Status: TodoInProgress, Valid: false},
		},
	}); err != nil {
		t.Fatal(err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || len(list[1].Todos) != 2 {
		t.Fatalf("list = %#v", list)
	}
	if list[1].Todos[0].Done != true || list[1].Todos[1].Valid != false {
		t.Fatalf("parsed todos = %#v", list[1].Todos)
	}

	merged := MergeTodos(list)
	if len(merged) != 2 {
		t.Fatalf("merged = %#v", merged)
	}
	if merged[0].ID != "1" || !merged[0].Done || merged[0].Status != TodoCompleted {
		t.Fatalf("merged[0] = %#v", merged[0])
	}
	if merged[1].ID != "2" || merged[1].Valid {
		t.Fatalf("merged[1] = %#v", merged[1])
	}
}

func TestReadForSessionPrefersHigherTurnOnEqualContent(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.Local)
	for _, entry := range []Entry{
		{Keywords: []string{"build", "cli"}, Summary: "Build the CLI with go build ./cmd/solcode.", Turn: 2, Time: base.Add(time.Hour), SessionID: "main"},
		{Keywords: []string{"build", "cli"}, Summary: "Build the CLI with go build ./cmd/solcode.", Turn: 9, Time: base, SessionID: "main"},
		{Keywords: []string{"unrelated"}, Summary: "Something else entirely.", Turn: 20, Time: base.Add(2 * time.Hour), SessionID: "main"},
	} {
		if _, err := store.Append(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := store.ReadForSession(ctx, "main", "build cli", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 {
		t.Fatalf("hits = %#v", hits)
	}
	if hits[0].Turn != 9 {
		t.Fatalf("first hit turn = %d, want 9 (higher turn wins with 50%% weight)", hits[0].Turn)
	}
}

func TestUpsertBySessionTurnMergesSameTurn(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.Local)

	first, replaced, err := store.UpsertBySessionTurn(ctx, Entry{
		Keywords:   []string{"todolist"},
		Summary:    "Todolist update (2 items).",
		Importance: 0.35,
		Turn:       7,
		Time:       base,
		SessionID:  "main",
		Files:      []string{"internal/a.go"},
		Todos: []TodoJudgment{
			{ID: "1", Content: "First", Status: TodoInProgress, Valid: true},
			{ID: "2", Content: "Second", Status: TodoPending, Valid: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("first upsert should append")
	}
	if first.Turn != 7 || first.SessionID != "main" {
		t.Fatalf("first = %#v", first)
	}

	second, replaced, err := store.UpsertBySessionTurn(ctx, Entry{
		Keywords:   []string{"todo-write"},
		Summary:    "Todolist update (2 items).",
		Importance: 0.4,
		Turn:       7,
		Time:       base.Add(time.Minute),
		SessionID:  "main",
		Files:      []string{"internal/b.go"},
		Todos: []TodoJudgment{
			{ID: "1", Content: "First", Status: TodoCompleted, Valid: true, Done: true},
			{ID: "2", Content: "Second", Status: TodoInProgress, Valid: false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("second upsert should replace same session+turn")
	}
	if len(second.Keywords) != 2 {
		t.Fatalf("keywords = %#v", second.Keywords)
	}
	if len(second.Files) != 2 {
		t.Fatalf("files = %#v", second.Files)
	}
	if len(second.Todos) != 2 || !second.Todos[0].Done || second.Todos[1].Valid {
		t.Fatalf("todos = %#v", second.Todos)
	}
	if second.Importance != 0.4 || !second.Time.Equal(base.Add(time.Minute)) {
		t.Fatalf("metadata = %#v", second)
	}

	other, replaced, err := store.UpsertBySessionTurn(ctx, Entry{
		Keywords:  []string{"turn"},
		Summary:   "Next turn memory.",
		Turn:      8,
		Time:      base.Add(2 * time.Minute),
		SessionID: "main",
		Todos: []TodoJudgment{
			{ID: "1", Content: "First", Status: TodoCompleted, Valid: true, Done: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("different turn should append")
	}
	if other.Turn != 8 {
		t.Fatalf("other = %#v", other)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %#v, want one entry per turn", list)
	}
	if list[0].Turn != 7 || list[1].Turn != 8 {
		t.Fatalf("turns = %#v", list)
	}
	if len(list[0].Todos) != 2 || list[0].Todos[0].Status != TodoCompleted {
		t.Fatalf("turn 7 todos = %#v", list[0].Todos)
	}
}

func TestReadForSessionFiltersBySessionID(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local)
	for _, entry := range []Entry{
		{Keywords: []string{"a"}, Summary: "Session A note.", Turn: 0, Time: base, SessionID: "session-a"},
		{Keywords: []string{"b"}, Summary: "Session B note about rewind.", Turn: 1, Time: base.Add(time.Hour), SessionID: "session-b"},
		{Keywords: []string{"a", "rewind"}, Summary: "Session A also mentions rewind.", Turn: 2, Time: base.Add(2 * time.Hour), SessionID: "session-a"},
	} {
		if _, err := store.Append(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}

	recent, err := store.ReadForSession(ctx, "session-a", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].Turn != 2 || recent[1].Turn != 0 {
		t.Fatalf("recent = %#v", recent)
	}

	hits, err := store.ReadForSession(ctx, "session-b", "rewind", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].SessionID != "session-b" {
		t.Fatalf("hits = %#v", hits)
	}

	unscoped, err := store.Read(ctx, "rewind", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(unscoped) != 2 {
		t.Fatalf("unscoped = %#v", unscoped)
	}
}
