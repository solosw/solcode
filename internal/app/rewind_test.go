package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

func TestRewindCodeRestoresFilesWithoutTouchingSessionMessages(t *testing.T) {
	work := t.TempDir()
	sessionDir := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: sessionDir, DefaultSession: "main"},
	}}

	a.beginCheckpointTurn("main", work, "first")
	original := "v1"
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	content := original
	a.captureCheckpoint(filepath.Join(work, "a.txt"), &content)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	a.beginCheckpointTurn("main", work, "create")
	a.captureCheckpoint(filepath.Join(work, "b.txt"), nil)
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := a.RewindCode("main", work, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Restored) != 1 || result.Restored[0] != "a.txt" {
		t.Fatalf("restored = %#v", result.Restored)
	}
	if len(result.Deleted) != 1 || result.Deleted[0] != "b.txt" {
		t.Fatalf("deleted = %#v", result.Deleted)
	}
	got, err := os.ReadFile(filepath.Join(work, "a.txt"))
	if err != nil || string(got) != original {
		t.Fatalf("a.txt = %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(work, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt should be gone, err=%v", err)
	}
}

func TestRewindCodeByName(t *testing.T) {
	work := t.TempDir()
	sessionDir := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: sessionDir, DefaultSession: "main"},
	}}

	a.beginCheckpointTurn("main", work, "first")
	original := "v1"
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	content := original
	a.captureCheckpoint(filepath.Join(work, "a.txt"), &content)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.NameCheckpoint("main", work, -1, "safe"); err != nil {
		t.Fatal(err)
	}

	a.beginCheckpointTurn("main", work, "create")
	a.captureCheckpoint(filepath.Join(work, "b.txt"), nil)
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, turn, err := a.RewindCodeByName("main", work, "safe")
	if err != nil {
		t.Fatal(err)
	}
	if turn != 0 {
		t.Fatalf("turn = %d", turn)
	}
	if len(result.Restored) != 1 || result.Restored[0] != "a.txt" {
		t.Fatalf("restored = %#v", result.Restored)
	}
	got, err := os.ReadFile(filepath.Join(work, "a.txt"))
	if err != nil || string(got) != original {
		t.Fatalf("a.txt = %q err=%v", got, err)
	}
}

func TestRewindCodeRestoresBashFingerprintCapture(t *testing.T) {
	work := t.TempDir()
	sessionDir := t.TempDir()
	a := &App{Config: config.Config{
		WorkDir: work,
		Session: config.SessionConfig{Dir: sessionDir, DefaultSession: "main"},
	}}

	if err := os.WriteFile(filepath.Join(work, "shell.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.beginCheckpointTurn("main", work, "bash mutate")

	before, err := tool.SnapshotWorkDir(work, tool.FingerprintOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "shell.txt"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "shell-new.txt"), []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := tool.SnapshotWorkDir(work, tool.FingerprintOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range tool.DiffFingerprints(before, after) {
		a.captureCheckpoint(filepath.Join(work, filepath.FromSlash(change.Path)), change.Content)
	}

	result, err := a.RewindCode("main", work, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Restored) != 1 || result.Restored[0] != "shell.txt" {
		t.Fatalf("restored = %#v", result.Restored)
	}
	if len(result.Deleted) != 1 || result.Deleted[0] != "shell-new.txt" {
		t.Fatalf("deleted = %#v", result.Deleted)
	}
	got, err := os.ReadFile(filepath.Join(work, "shell.txt"))
	if err != nil || string(got) != "before" {
		t.Fatalf("shell.txt = %q err=%v", got, err)
	}
}
