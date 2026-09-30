package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/sessionmemory"
	"github.com/solosw/solcode/internal/tool"
)

func TestWriteSessionMemoryRecordsCheckpointContext(t *testing.T) {
	work := t.TempDir()
	sessionDir := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: sessionDir, DefaultSession: "main"},
	}}

	// Open a checkpoint turn and capture one changed file.
	a.beginCheckpointTurn("main", work, "do work")
	content := "before"
	a.captureCheckpoint(filepath.Join(work, "internal", "app", "app.go"), &content)

	// An earlier turn in the same session contributes another file; a session-end
	// memory should report both.
	a.beginCheckpointTurn("main", work, "second work")
	a.captureCheckpoint(filepath.Join(work, "internal", "tool", "read_memory.go"), &content)

	result, err := a.WriteSessionMemory(context.Background(), tool.SessionMemoryWriteRequest{
		Keywords:   []string{"session-memory", "solcode.md"},
		Summary:    "Implemented session memory writing into solcode.md.",
		Importance: 0.7,
		SessionID:  "main",
		WorkDir:    work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Stored {
		t.Fatalf("not stored: %#v", result)
	}
	if result.Turn != 1 {
		t.Fatalf("turn = %d, want 1 (in-progress turn)", result.Turn)
	}
	if len(result.Files) != 2 {
		t.Fatalf("files = %#v, want both files across the session", result.Files)
	}
	if result.SessionID != "main" {
		t.Fatalf("session = %q", result.SessionID)
	}

	raw, err := os.ReadFile(sessionmemory.Path(work))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"turn 1", "session main", "internal/app/app.go",
		"internal/tool/read_memory.go", "Implemented session memory",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

func TestRecordTurnSessionMemoryCapturesTodosAndPrunesNoise(t *testing.T) {
	work := t.TempDir()
	sessionDir := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: sessionDir, DefaultSession: "main"},
	}}

	todoPath := config.DefaultTodoPath(work)
	if err := os.MkdirAll(filepath.Dir(todoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	todoJSON := `[
  {"id":"1","content":"Wire turn end","status":"in_progress","priority":"high"},
  {"id":"2","content":"Prune files","status":"pending","priority":"medium"}
]`
	if err := os.WriteFile(todoPath, []byte(todoJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	a.beginCheckpointTurn("main", work, "wire turn memory")
	content := "body"
	a.captureCheckpoint(filepath.Join(work, "internal", "app", "session_memory.go"), &content)
	a.captureCheckpoint(filepath.Join(work, ".solcode", "solcode.md"), &content)
	a.captureCheckpoint(filepath.Join(work, "go.sum"), &content)
	a.captureCheckpoint(filepath.Join(work, "tmp", "noise.log"), &content)

	// Model outcome is preferred over the raw user prompt.
	a.recordTurnSessionMemory(context.Background(), "main", work, "嗯随便看看", "Wired turn-end session memory and pruned noise files.")

	entries, err := sessionmemory.NewStore(work).ReadForSession(context.Background(), "main", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	entry := entries[0]
	if !strings.Contains(entry.Summary, "Wired turn-end session memory") {
		t.Fatalf("summary = %q, want model outcome", entry.Summary)
	}
	if strings.Contains(entry.Summary, "随便看看") {
		t.Fatalf("summary kept raw user prompt noise: %q", entry.Summary)
	}
	if len(entry.Files) != 1 || entry.Files[0] != "internal/app/session_memory.go" {
		t.Fatalf("files = %#v, want only the source file after prune", entry.Files)
	}
	if len(entry.Todos) != 2 {
		t.Fatalf("todos = %#v", entry.Todos)
	}
	if entry.Todos[0].ID != "1" || !entry.Todos[0].Valid || entry.Todos[0].Done {
		t.Fatalf("todo0 = %#v", entry.Todos[0])
	}
	if entry.Todos[0].Status != sessionmemory.TodoInProgress {
		t.Fatalf("todo0 status = %q", entry.Todos[0].Status)
	}
}

func TestTurnSessionMemorySummaryPrefersModelOutcome(t *testing.T) {
	got := turnSessionMemorySummary("用户随便说的废话", "Fixed the organizer turn summary path.")
	if got != "Fixed the organizer turn summary path." {
		t.Fatalf("got %q", got)
	}
	// Fallback to prompt when the model produced nothing.
	if got := turnSessionMemorySummary("only prompt left", "  "); got != "only prompt left" {
		t.Fatalf("fallback = %q", got)
	}
	// Under the per-turn cap, text is kept whole.
	short := strings.Repeat("字", turnSessionMemoryMaxRunes)
	if got := turnSessionMemorySummary("", short); got != short {
		t.Fatalf("under-cap should stay whole, got %d runes", len([]rune(got)))
	}
	// Over the cap: keep problem head + status tail (not a mid-word head-only cut).
	head := "Problem: n_batch assert killed the generate worker after long organizer prompts. "
	tail := " Status: DecodeTokensInBatches fixed generate_long_cpu and generate_long_gpu; staged solcode.new.exe pending swap."
	mid := strings.Repeat("detail filler about intermediate debug steps. ", 80)
	long := head + mid + tail
	got = turnSessionMemorySummary("", long)
	if !strings.Contains(got, " … ") {
		t.Fatalf("expected head/tail ellipsis, got %q", got)
	}
	if n := len([]rune(got)); n > turnSessionMemoryMaxRunes {
		t.Fatalf("truncation length = %d, want <= %d", n, turnSessionMemoryMaxRunes)
	}
	if !strings.HasPrefix(got, "Problem:") {
		t.Fatalf("head (problem) missing: %q", got[:min(80, len(got))])
	}
	if !strings.Contains(got, "staged solcode.new.exe pending swap") {
		t.Fatalf("tail (status) missing: %q", got)
	}
}

func TestTruncateSessionSummaryHeadTail(t *testing.T) {
	if got := truncateSessionSummaryHeadTail("short", 100); got != "short" {
		t.Fatalf("got %q", got)
	}
	in := strings.Repeat("a", 50) + "MID" + strings.Repeat("z", 50)
	got := truncateSessionSummaryHeadTail(in, 40)
	if !strings.Contains(got, " … ") {
		t.Fatalf("got %q", got)
	}
	if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "zzz") {
		t.Fatalf("head/tail lost: %q", got)
	}
	if n := len([]rune(got)); n > 40 {
		t.Fatalf("len=%d", n)
	}
}

func TestFilterUnimportantSessionFiles(t *testing.T) {
	in := []string{
		"internal/app/app.go",
		".solcode/solcode.md",
		"node_modules/pkg/index.js",
		"go.sum",
		"tmp/debug.log",
		"dist/bundle.min.js",
		"internal/app/app.go",
		"",
	}
	got := filterUnimportantSessionFiles(in)
	if len(got) != 1 || got[0] != "internal/app/app.go" {
		t.Fatalf("got = %#v", got)
	}
}

func TestRecordTodoSessionMemoryMergesSameTurn(t *testing.T) {
	work := t.TempDir()
	sessionDir := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: sessionDir, DefaultSession: "main"},
	}}
	a.beginCheckpointTurn("main", work, "multi todo updates")

	a.recordTodoSessionMemory(context.Background(), "main", work, []tool.TodoItem{
		{ID: "1", Content: "First", Status: "in_progress", Priority: "high"},
		{ID: "2", Content: "Second", Status: "pending", Priority: "medium"},
	})
	a.recordTodoSessionMemory(context.Background(), "main", work, []tool.TodoItem{
		{ID: "1", Content: "First", Status: "completed", Priority: "high"},
		{ID: "2", Content: "Second", Status: "in_progress", Priority: "medium"},
	})

	entries, err := sessionmemory.NewStore(work).ReadForSession(context.Background(), "main", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v, want one entry for the same session turn", entries)
	}
	entry := entries[0]
	if len(entry.Todos) != 2 {
		t.Fatalf("todos = %#v", entry.Todos)
	}
	if entry.Todos[0].Status != sessionmemory.TodoCompleted || !entry.Todos[0].Done {
		t.Fatalf("todo0 = %#v", entry.Todos[0])
	}
	if entry.Todos[1].Status != sessionmemory.TodoInProgress {
		t.Fatalf("todo1 = %#v", entry.Todos[1])
	}
	if !strings.Contains(entry.Summary, "Todolist update (2 items)") {
		t.Fatalf("summary = %q", entry.Summary)
	}
}

func TestReadSessionMemoryFuzzy(t *testing.T) {
	work := t.TempDir()
	sessionDir := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: sessionDir, DefaultSession: "main"},
	}}

	a.beginCheckpointTurn("main", work, "checkpoint work")
	if _, err := a.WriteSessionMemory(context.Background(), tool.SessionMemoryWriteRequest{
		Keywords:  []string{"checkpoint"},
		Summary:   "Checkpoints restore code only.",
		WorkDir:   work,
		SessionID: "main",
	}); err != nil {
		t.Fatal(err)
	}

	a.beginCheckpointTurn("main", work, "mcp work")
	if _, err := a.WriteSessionMemory(context.Background(), tool.SessionMemoryWriteRequest{
		Keywords:  []string{"mcp"},
		Summary:   "MCP uses STREAMABLE_HTTP.",
		WorkDir:   work,
		SessionID: "main",
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := a.ReadSessionMemory(context.Background(), tool.SessionMemoryReadRequest{Query: "checkpoint", Limit: 5, WorkDir: work, SessionID: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits.Entries) != 1 || !strings.Contains(hits.Entries[0].Summary, "Checkpoints") {
		t.Fatalf("hits = %#v", hits.Entries)
	}

	recent, err := a.ReadSessionMemory(context.Background(), tool.SessionMemoryReadRequest{Limit: 1, WorkDir: work, SessionID: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent.Entries) != 1 || !strings.Contains(recent.Entries[0].Summary, "MCP") {
		t.Fatalf("recent = %#v", recent.Entries)
	}
}

func TestReadSessionMemoryScopedToCurrentSession(t *testing.T) {
	work := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: t.TempDir(), DefaultSession: "session-b"},
	}}

	if _, err := a.WriteSessionMemory(context.Background(), tool.SessionMemoryWriteRequest{
		Keywords:  []string{"other"},
		Summary:   "Other session recorded a computer-use fix.",
		WorkDir:   work,
		SessionID: "session-a",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.WriteSessionMemory(context.Background(), tool.SessionMemoryWriteRequest{
		Keywords:  []string{"current"},
		Summary:   "Current session recorded session-memory scoping.",
		WorkDir:   work,
		SessionID: "session-b",
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := a.ReadSessionMemory(context.Background(), tool.SessionMemoryReadRequest{
		Query:     "session",
		Limit:     5,
		WorkDir:   work,
		SessionID: "session-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits.Entries) != 1 {
		t.Fatalf("hits = %#v", hits.Entries)
	}
	if hits.Entries[0].SessionID != "session-b" {
		t.Fatalf("session = %q", hits.Entries[0].SessionID)
	}
	if !strings.Contains(hits.Entries[0].Summary, "session-memory scoping") {
		t.Fatalf("summary = %q", hits.Entries[0].Summary)
	}

	// Empty SessionID falls back to Config.Session.DefaultSession.
	defaultHits, err := a.ReadSessionMemory(context.Background(), tool.SessionMemoryReadRequest{
		Limit:   5,
		WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultHits.Entries) != 1 || defaultHits.Entries[0].SessionID != "session-b" {
		t.Fatalf("defaultHits = %#v", defaultHits.Entries)
	}
}
