package projectindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchSymbolsFindsDeclarations(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\n\nfunc RunMessagesLoop() {}\n\ntype Widget struct{}\n")
	writeFile(t, dir, "b.py", "def run_messages_loop():\n    pass\n")
	writeFile(t, dir, "notes.md", "func NotSource()\n")

	hits := SearchSymbols(context.Background(), dir, "MessagesLoop", 10)
	if len(hits) == 0 {
		t.Fatal("expected symbol hits")
	}
	found := false
	for _, hit := range hits {
		if hit.Name == "RunMessagesLoop" && hit.Kind == "func" && hit.Path == "a.go" {
			found = true
		}
		if filepath.Ext(hit.Path) == ".md" {
			t.Fatalf("non-source file scanned: %+v", hit)
		}
	}
	if !found {
		t.Fatalf("RunMessagesLoop not found: %+v", hits)
	}
}

func TestRecentPathsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "old.go", "package old\n")
	writeFile(t, dir, "new.go", "package new\n")
	paths := RecentPaths(context.Background(), dir, 5)
	if len(paths) == 0 {
		t.Fatal("expected recent paths")
	}
	if paths[0] != "new.go" {
		t.Fatalf("newest path = %s, want new.go (%v)", paths[0], paths)
	}
}

func TestContextBoundedAndLabelled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "engine.go", "package engine\n\nfunc runMessagesLoop() {}\n")
	text := Context(context.Background(), dir, "runMessagesLoop")
	if text == "" {
		t.Fatal("expected context text")
	}
	if !strings.Contains(text, "verify") {
		t.Fatalf("context must be labelled as unverified: %s", text)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
