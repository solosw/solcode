package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/codegraphruntime/bundle"
)

func smokeRuntime(t *testing.T) (bundle.Runtime, string) {
	t.Helper()
	if os.Getenv("SOLCODE_CODEGRAPH_SMOKE") != "1" {
		t.Skip("opt-in actual native runtime test")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache := t.TempDir()
	r, err := bundle.Resolve(context.Background(), os.Getenv("SOLCODE_CODEGRAPH_RUNTIME_DIR"), cache)
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(r.Binary, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "codegraph 0.55.2" {
		t.Fatalf("native version: %s %v", version, err)
	}
	return r, cache
}

func smokeIndexDir(t *testing.T, root string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return codeGraphEnv(canonical)["CODEGRAPH_DIR"]
}

func smokeQuery(t *testing.T, c *CodeGraphClient, name, raw string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := c.CallTool(ctx, name, json.RawMessage(raw))
	if err != nil || r == nil || r.IsError {
		t.Fatalf("%s: %+v %v", name, r, err)
	}
	return r.Text
}
func smokeStart(t *testing.T, c *CodeGraphClient) {
	t.Helper()
	c.Start()
	select {
	case <-c.done:
	case <-time.After(90 * time.Second):
		t.Fatal("native startup timed out")
	}
	if c.err != nil {
		t.Fatal(c.err)
	}
}

// The daemon is test-owned, but never kill it: a short test-only idle timeout
// demonstrates that closing proxies releases services normally before cleanup.
func waitSmokeDaemonExit(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := os.Stat(filepath.Join(smokeIndexDir(t, root), "daemon.pid"))
		_, writerErr := os.Stat(filepath.Join(smokeIndexDir(t, root), "writer.json"))
		if os.IsNotExist(err) && os.IsNotExist(writerErr) {
			return
		}
		if time.Now().After(deadline) {
			t.Error("test daemon did not idle-exit after closing all proxies")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestCodeGraphRuntimeSmoke(t *testing.T) {
	_, cache := smokeRuntime(t)
	t.Setenv("CODEGRAPH_DAEMON_IDLE_TIMEOUT_MS", "1000")
	t.Setenv("RUST_LOG", "codegraph_watch=debug,codegraph_daemon=debug")
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Fatal(err)
	}
	ignore := []byte("# existing ignore rules\n")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), ignore, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(filepath.Join(root, ".git", "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	// An older engine's data must never be opened, migrated or removed.
	if err := os.Mkdir(filepath.Join(root, ".codegraph"), 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, ".codegraph", "codegraph.db")
	if err := os.WriteFile(old, []byte("previous Node engine index"), 0600); err != nil {
		t.Fatal(err)
	}
	legacyNative := filepath.Join(root, ".solcode-codegraph")
	if err := os.Mkdir(legacyNative, 0700); err != nil {
		t.Fatal(err)
	}
	nativeSentinel := filepath.Join(legacyNative, "codegraph.db")
	if err := os.WriteFile(nativeSentinel, []byte("previous native engine index"), 0600); err != nil {
		t.Fatal(err)
	}
	code := filepath.Join(root, "main.ts")
	if err := os.WriteFile(code, []byte("export function smokeLeaf() { return 1; }\nexport function smokeCaller() { return smokeLeaf(); }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := NewCodeGraphClient(root, os.Getenv("SOLCODE_CODEGRAPH_RUNTIME_DIR"), cache, nil)
	b := NewCodeGraphClient(root, os.Getenv("SOLCODE_CODEGRAPH_RUNTIME_DIR"), cache, nil)
	a.Start()
	b.Start()
	t.Cleanup(func() {
		if t.Failed() {
			log, _ := os.ReadFile(filepath.Join(smokeIndexDir(t, root), "daemon.log"))
			t.Logf("daemon diagnostics: %s", log)
		}
		_ = a.Close()
		_ = b.Close()
		waitSmokeDaemonExit(t, root)
	})
	smokeStart(t, a)
	smokeStart(t, b)
	result := smokeQuery(t, a, "codegraph_search", `{"query":"smokeLeaf"}`)
	if !strings.Contains(result, "main.ts") || strings.Contains(result, "No results") {
		t.Fatal("missing initial index:", result)
	}
	callers := smokeQuery(t, a, "codegraph_callers", `{"symbol":"smokeLeaf"}`)
	if !strings.Contains(callers, "smokeCaller") {
		t.Fatal("missing call graph:", callers)
	}
	// Exercise all eight exposed query contracts against the actual native server.
	for _, q := range []struct{ name, raw string }{
		{"codegraph_explore", `{"query":"smokeCaller smokeLeaf","maxFiles":2}`},
		{"codegraph_callees", `{"symbol":"smokeCaller"}`},
		{"codegraph_impact", `{"symbol":"smokeLeaf","depth":2}`},
		{"codegraph_files", "{}"}, {"codegraph_status", "{}"},
	} {
		smokeQuery(t, a, q.name, q.raw)
	}
	// writer.pid is the intentionally empty kernel lock; diagnostics are separate.
	writerRaw, err := os.ReadFile(filepath.Join(smokeIndexDir(t, root), "writer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var writer struct {
		PID  int    `json:"pid"`
		Mode string `json:"mode"`
	}
	if json.Unmarshal(writerRaw, &writer) != nil || writer.PID <= 0 || writer.Mode != "daemon" {
		t.Fatalf("not a shared daemon: %s", writerRaw)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	result = smokeQuery(t, b, "codegraph_node", `{"symbol":"smokeLeaf","includeCode":true}`)
	if !strings.Contains(result, "return 1") {
		t.Fatal("close affected another session:", result)
	}
	still, err := os.ReadFile(filepath.Join(smokeIndexDir(t, root), "writer.json"))
	if err != nil || string(still) != string(writerRaw) {
		t.Fatal("shared writer changed")
	}
	if err := os.WriteFile(code, []byte("export function smokeFresh() { return 2; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		result = smokeQuery(t, b, "codegraph_search", `{"query":"smokeFresh"}`)
		if strings.Contains(result, "main.ts") && !strings.Contains(result, "No results") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher missed changed symbol:", result)
		}
		time.Sleep(500 * time.Millisecond)
	}
	result = smokeQuery(t, b, "codegraph_search", `{"query":"smokeLeaf"}`)
	if !strings.Contains(result, "No results") {
		t.Fatal("retained removed symbol:", result)
	}
	_ = b.Close()
	waitSmokeDaemonExit(t, root)
	if err := os.WriteFile(code, []byte("export function smokeOffline() { return 3; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := NewCodeGraphClient(root, os.Getenv("SOLCODE_CODEGRAPH_RUNTIME_DIR"), cache, nil)
	t.Cleanup(func() { _ = c.Close(); waitSmokeDaemonExit(t, root) })
	smokeStart(t, c)
	// Upstream startup catch-up is asynchronous, just like watcher updates.
	deadline = time.Now().Add(25 * time.Second)
	for {
		result = smokeQuery(t, c, "codegraph_search", `{"query":"smokeOffline"}`)
		if strings.Contains(result, "main.ts") && !strings.Contains(result, "No results") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("offline catch-up missed:", result)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := os.Remove(code); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(25 * time.Second)
	for {
		result = smokeQuery(t, c, "codegraph_search", `{"query":"smokeOffline"}`)
		if strings.Contains(result, "No results") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher retained deleted file:", result)
		}
		time.Sleep(100 * time.Millisecond)
	}
	after, err := os.ReadDir(filepath.Join(root, ".git", "hooks"))
	if err != nil || len(after) != len(before) {
		t.Fatal("modified hooks")
	}
	got, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil || string(got) != string(ignore) {
		t.Fatal("modified root ignore file")
	}
	got, err = os.ReadFile(old)
	if err != nil || string(got) != "previous Node engine index" {
		t.Fatal("modified legacy index")
	}
	got, err = os.ReadFile(nativeSentinel)
	if err != nil || string(got) != "previous native engine index" {
		t.Fatal("modified legacy native index")
	}
	entries, err := os.ReadDir(legacyNative)
	if err != nil || len(entries) != 1 {
		t.Fatal("wrote new data to legacy native index")
	}
	for _, name := range []string{".mcp.json", "AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("installed agent configuration", name)
		}
	}
	t.Log("native runtime: initial index, eight queries, shared daemon, edits/removals, offline catch-up and no installer side effects verified")
}

// Several cold clients must not mistake a concurrent init's temporary DB gap
// for a permanently unindexed project while they acquire live service leases.
func TestCodeGraphConcurrentColdStartSmoke(t *testing.T) {
	_, cache := smokeRuntime(t)
	t.Setenv("CODEGRAPH_DAEMON_IDLE_TIMEOUT_MS", "1000")
	root := t.TempDir()
	var source strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&source, "export function concurrentSymbol%d() { return %d; }\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(root, "concurrent.ts"), []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	clients := make([]*CodeGraphClient, 6)
	t.Cleanup(func() {
		if t.Failed() {
			log, _ := os.ReadFile(filepath.Join(smokeIndexDir(t, root), "daemon.log"))
			t.Logf("daemon diagnostics: %s", log)
		}
		for _, c := range clients {
			if c != nil {
				_ = c.Close()
			}
		}
		waitSmokeDaemonExit(t, root)
	})
	for i := range clients {
		clients[i] = NewCodeGraphClient(root, os.Getenv("SOLCODE_CODEGRAPH_RUNTIME_DIR"), cache, nil)
		clients[i].Start()
	}
	for _, c := range clients {
		smokeStart(t, c)
		result := smokeQuery(t, c, "codegraph_search", `{"query":"concurrentSymbol999"}`)
		if !strings.Contains(result, "concurrent.ts") {
			t.Fatal("missing concurrent index:", result)
		}
	}
}

func TestCodeGraphInitializationRecoverySmoke(t *testing.T) {
	r, _ := smokeRuntime(t)
	root := t.TempDir()
	var source strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&source, "export function initSymbol%d() { return %d; }\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(root, "large.ts"), []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- initializeCodeGraph(ctx, r, root, codeGraphEnv(root)) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		building := false
		for _, name := range []string{"index-state.0.json", "index-state.1.json"} {
			raw, _ := os.ReadFile(filepath.Join(smokeIndexDir(t, root), name))
			if strings.Contains(string(raw), "building") {
				building = true
			}
		}
		if building {
			cancel()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("init finished before interruption: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("did not observe upstream building state")
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-done; err == nil {
		t.Fatal("initial scan was not interrupted")
	}
	if err := initializeCodeGraph(context.Background(), r, root, codeGraphEnv(root)); err != nil {
		t.Fatal("interrupted initial scan recovery:", err)
	}
	t.Log("native init recovered its own interrupted building state")
}

func TestCodeGraphBootstrapSafetySmoke(t *testing.T) {
	r, _ := smokeRuntime(t)
	t.Run("foreign database", func(t *testing.T) {
		root := t.TempDir()
		dir := smokeIndexDir(t, root)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		db := filepath.Join(dir, "codegraph.db")
		original := []byte("foreign database must not be overwritten")
		if err := os.WriteFile(db, original, 0600); err != nil {
			t.Fatal(err)
		}
		if err := initializeCodeGraph(context.Background(), r, root, codeGraphEnv(root)); err == nil {
			t.Fatal("accepted foreign database")
		}
		got, err := os.ReadFile(db)
		if err != nil || string(got) != string(original) {
			t.Fatal("modified foreign database")
		}
	})
	t.Run("different project owner", func(t *testing.T) {
		root, other := t.TempDir(), t.TempDir()
		env := codeGraphEnv(root)
		if err := initializeCodeGraph(context.Background(), r, root, env); err != nil {
			t.Fatal(err)
		}
		db := filepath.Join(env["CODEGRAPH_DIR"], "codegraph.db")
		before, err := os.ReadFile(db)
		if err != nil {
			t.Fatal(err)
		}
		if err := initializeCodeGraph(context.Background(), r, other, env); err == nil {
			t.Fatal("accepted another project's external index")
		}
		after, err := os.ReadFile(db)
		if err != nil || string(before) != string(after) {
			t.Fatal("modified another project's database")
		}
	})
	t.Run("symlinked index", func(t *testing.T) {
		root, out := t.TempDir(), t.TempDir()
		if err := os.MkdirAll(filepath.Dir(smokeIndexDir(t, root)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(out, smokeIndexDir(t, root)); err != nil {
			t.Skip(err)
		}
		if err := initializeCodeGraph(context.Background(), r, root, codeGraphEnv(root)); err == nil {
			t.Fatal("accepted symlinked index")
		}
		entries, err := os.ReadDir(out)
		if err != nil || len(entries) != 0 {
			t.Fatal("wrote outside project")
		}
	})
}
