package engine

import (
	"strings"
	"testing"
)

func TestWorkingSetObserveAndInvalidate(t *testing.T) {
	ws := NewWorkingSet()
	ws.ObserveFile("/tmp/a.go", []byte("package a\n"), true, false)
	if text := ws.InjectionText(); !strings.Contains(text, "/tmp/a.go") {
		t.Fatalf("observed file missing from injection: %s", text)
	}
	if _, ok := ws.Hash("/tmp/a.go"); !ok {
		t.Fatal("hash not recorded")
	}
	// A mutated observe is recorded with the fresh post-edit content, and the
	// file is marked modified/unverified.
	ws.ObserveFile("/tmp/a.go", []byte("package a // changed\n"), true, true)
	if cached, ok := ws.Lookup("/tmp/a.go"); !ok || !strings.Contains(cached, "changed") {
		t.Fatalf("mutated observe should cache fresh content, got %q ok=%v", cached, ok)
	}
	text := ws.InjectionText()
	if !strings.Contains(text, "modified, unverified") {
		t.Fatalf("modified flag missing: %s", text)
	}
	ws.MarkVerified("/tmp/a.go")
	if text := ws.InjectionText(); !strings.Contains(text, "modified, verified") {
		t.Fatalf("verified flag missing: %s", text)
	}
}

func TestWorkingSetInvalidateDropsCache(t *testing.T) {
	ws := NewWorkingSet()
	ws.ObserveFile("/tmp/b.go", []byte("package b\n"), true, false)
	if _, ok := ws.Lookup("/tmp/b.go"); !ok {
		t.Fatal("expected cached content")
	}
	ws.Invalidate("/tmp/b.go")
	if _, ok := ws.Lookup("/tmp/b.go"); ok {
		t.Fatal("invalidated content must not be returned")
	}
}

func TestWorkingSetRecoveryTextOnlyForCachedPaths(t *testing.T) {
	ws := NewWorkingSet()
	ws.ObserveFile("/tmp/c.go", []byte("package c\n"), true, false)
	ws.ObserveFile("/tmp/big.go", make([]byte, defaultWorkingSetCacheMax+10), true, false)

	recovered := ws.RecoveryText([]string{"/tmp/c.go", "/tmp/big.go", "/tmp/missing.go"})
	if !strings.Contains(recovered, "package c") {
		t.Fatalf("cached content not recovered: %s", recovered)
	}
	if strings.Contains(recovered, "/tmp/big.go") {
		t.Fatalf("oversized file must not be cached/recovered: %s", recovered)
	}
}

func TestWorkingSetEvictionBounds(t *testing.T) {
	ws := NewWorkingSet()
	ws.maxFiles = 3
	for i := 0; i < 10; i++ {
		ws.BeginTurn(i)
		ws.ObserveFile(filepathFor(i), []byte("x"), true, false)
	}
	if len(ws.files) > 3 {
		t.Fatalf("file set exceeded bound: %d", len(ws.files))
	}
}

func filepathFor(i int) string {
	return "/tmp/evict_" + string(rune('a'+i)) + ".go"
}
