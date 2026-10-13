package engine

import (
	"bytes"
	"fmt"
	"github.com/solosw/solcode/internal/tool"
	"os"
	"path/filepath"
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

func TestWorkingSetGoalAloneHasNoContext(t *testing.T) {
	ws := NewWorkingSet()
	ws.SetGoal("task already in prompt")
	if ws.HasContext() {
		t.Fatal("goal alone should not cause injection")
	}
	ws.AddFact("confirmed tool result")
	if !ws.HasContext() {
		t.Fatal("confirmed facts should qualify for injection")
	}
}

func TestWorkingSetLargeFileIsMetadataOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.go")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), defaultWorkingSetCacheMax+100), 0600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkingSet()
	observeWorkingFilePath(ws, &tool.UseContext{}, path, false)
	if _, ok := ws.Lookup(path); ok {
		t.Fatal("an oversized prefix must not masquerade as the full cached file")
	}
	if ws.RecoveryText([]string{path}) != "" || !strings.Contains(ws.InjectionText(), "prefix hash") {
		t.Fatal("oversized observations should retain clearly labeled metadata only")
	}
}

func TestWorkingSetSkippedRevertedAndFailedWritesAreNotConfirmedMutations(t *testing.T) {
	for _, status := range []string{"skipped", "reverted", "error"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "a.go")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			ws := NewWorkingSet()
			ws.ObserveFile(path, []byte("original"), true, false)
			actualStatus, reverted := status, false
			if status == "reverted" {
				actualStatus, reverted = "success", true
			}
			result := ToolResult{Content: tool.Result(fmt.Sprintf(`{"results":[{"id":"op_1","status":%q,"reverted":%t}]}`, actualStatus, reverted))}
			observeBatchWorkingSet(ws, []byte(`{"operations":[{"tool":"Edit","input":{"path":"a.go"}}]}`), &tool.UseContext{WorkDir: dir}, result)
			if len(ws.ModifiedPaths()) != 0 {
				t.Fatal("failed/skipped/reverted writes are not confirmed mutations")
			}
			_, cached := ws.Lookup(path)
			if cached != (status != "error") {
				t.Fatalf("cache state for %s = %v", status, cached)
			}
			if status == "error" && !strings.Contains(ws.InjectionText(), "stale metadata") {
				t.Fatal("uncertain file state should be explicit")
			}
		})
	}
}
