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

func TestReadSessionMemoryFuzzy(t *testing.T) {
	work := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: t.TempDir(), DefaultSession: "main"},
	}}

	for _, entry := range []tool.SessionMemoryWriteRequest{
		{Keywords: []string{"checkpoint"}, Summary: "Checkpoints restore code only.", WorkDir: work},
		{Keywords: []string{"mcp"}, Summary: "MCP uses STREAMABLE_HTTP.", WorkDir: work},
	} {
		if _, err := a.WriteSessionMemory(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := a.ReadSessionMemory(context.Background(), tool.SessionMemoryReadRequest{Query: "checkpoint", Limit: 5, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits.Entries) != 1 || !strings.Contains(hits.Entries[0].Summary, "Checkpoints") {
		t.Fatalf("hits = %#v", hits.Entries)
	}

	recent, err := a.ReadSessionMemory(context.Background(), tool.SessionMemoryReadRequest{Limit: 1, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent.Entries) != 1 || !strings.Contains(recent.Entries[0].Summary, "MCP") {
		t.Fatalf("recent = %#v", recent.Entries)
	}
}
