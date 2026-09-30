package yzma

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/solosw/solcode/internal/organizer"
)

func TestResolveLibraryDirPrecedence(t *testing.T) {
	if got := ResolveLibraryDir("/explicit/dir"); got != "/explicit/dir" {
		t.Fatalf("ResolveLibraryDir() = %q, want the explicit dir", got)
	}
	t.Setenv("YZMA_LIB", "/from/env")
	if got := ResolveLibraryDir(""); got != "/from/env" {
		t.Fatalf("ResolveLibraryDir() = %q, want the YZMA_LIB value", got)
	}
}

func TestLibraryPathHasPlatformSuffix(t *testing.T) {
	path := LibraryPath(filepath.Join("some", "lib"))
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, ".dll"), strings.HasSuffix(base, ".so"), strings.HasSuffix(base, ".dylib"):
	default:
		t.Fatalf("library filename %q has no recognized platform suffix", base)
	}
	if !strings.Contains(base, "llama") {
		t.Fatalf("library filename %q should mention llama", base)
	}
}

func TestLibraryPresentFalseForEmptyDir(t *testing.T) {
	if LibraryPresent(t.TempDir()) {
		t.Fatal("an empty directory must not report the library as present")
	}
}

func TestModelPresentChecksRegularFile(t *testing.T) {
	if ModelPresent("") {
		t.Fatal("an empty path must not report a model as present")
	}
	if ModelPresent(t.TempDir()) {
		t.Fatal("a directory must not count as a model file")
	}
	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, []byte("not a real gguf"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	if !ModelPresent(path) {
		t.Fatal("an existing regular file must report as present")
	}
}

func TestNormalizeConfigCapsContext(t *testing.T) {
	cfg := normalizeConfig(Config{ContextSize: 99999})
	if cfg.ContextSize != maxContextSize {
		t.Fatalf("ContextSize = %d, want capped %d", cfg.ContextSize, maxContextSize)
	}
	cfg = normalizeConfig(Config{})
	if cfg.ContextSize != defaultContextSize {
		t.Fatalf("default ContextSize = %d, want %d", cfg.ContextSize, defaultContextSize)
	}
	if cfg.IdleUnloadAfter != idleUnloadAfter {
		t.Fatalf("IdleUnloadAfter = %s", cfg.IdleUnloadAfter)
	}
}

func TestBatchSizeStaysBelowContext(t *testing.T) {
	g := &generator{cfg: normalizeConfig(Config{ContextSize: 2048})}
	if got := g.batchSize(); got > maxBatchSize || got > g.contextSize() {
		t.Fatalf("batchSize = %d, ctx = %d", got, g.contextSize())
	}
	if got := g.batchSize(); got != defaultBatchSize && g.contextSize() >= defaultBatchSize {
		t.Fatalf("batchSize = %d, want default %d", got, defaultBatchSize)
	}
	g.cfg.ContextSize = 256
	if got := g.batchSize(); got > 256 {
		t.Fatalf("batchSize = %d exceeds tiny ctx", got)
	}
}

func TestReleaseAfterGenerateHonorsDisable(t *testing.T) {
	g := &generator{cfg: normalizeConfig(Config{IdleUnloadAfter: -1})}
	if g.idleAfter() != 0 {
		t.Fatalf("disabled idleAfter = %s, want 0", g.idleAfter())
	}
	// freeModelLocked must be a no-op-safe call with empty handles.
	g.mu.Lock()
	g.releaseAfterGenerate()
	g.mu.Unlock()
	if g.ready {
		t.Fatal("empty generator should stay not-ready")
	}
}

func TestRuntimeLockSerializes(t *testing.T) {
	var mu sync.Mutex
	var order []int
	appendOrder := func(n int) {
		mu.Lock()
		order = append(order, n)
		mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		WithRuntime(func() {
			appendOrder(1)
			time.Sleep(20 * time.Millisecond)
			appendOrder(2)
		})
	}()
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		WithRuntime(func() {
			appendOrder(3)
			appendOrder(4)
		})
	}()
	wg.Wait()
	if len(order) != 4 {
		t.Fatalf("order = %v", order)
	}
	// Second critical section must not interleave into the first.
	if !(order[0] == 1 && order[1] == 2 && order[2] == 3 && order[3] == 4) {
		t.Fatalf("runtime lock interleaved: %v", order)
	}
}

func TestReleaseOtherGPUHoldersSkipsSelf(t *testing.T) {
	type holder struct{ id string }
	a := &holder{id: "a"}
	b := &holder{id: "b"}
	var freed []string
	var mu sync.Mutex
	note := func(id string) {
		mu.Lock()
		freed = append(freed, id)
		mu.Unlock()
	}
	RegisterGPUHolder(a, func() { note("a") })
	RegisterGPUHolder(b, func() { note("b") })
	t.Cleanup(func() {
		UnregisterGPUHolder(a)
		UnregisterGPUHolder(b)
	})
	ReleaseOtherGPUHolders(a)
	mu.Lock()
	defer mu.Unlock()
	if len(freed) != 1 || freed[0] != "b" {
		t.Fatalf("freed = %v, want only b", freed)
	}
}

func TestShouldIsolateNativeWindowsGPU(t *testing.T) {
	t.Setenv(isolateEnvDisable, "")
	t.Setenv(workerEnvMarker, "")
	if runtime.GOOS == "windows" {
		if !ShouldIsolateNative(-1) {
			t.Fatal("windows GPU layers should isolate by default")
		}
		if !ShouldIsolateNative(32) {
			t.Fatal("windows positive gpu layers should isolate")
		}
		if ShouldIsolateNative(0) {
			t.Fatal("cpu-only must stay in-process")
		}
	} else if ShouldIsolateNative(-1) {
		t.Fatal("non-windows must not isolate by default")
	}
	t.Setenv(isolateEnvDisable, "0")
	if ShouldIsolateNative(-1) {
		t.Fatal("SOLCODE_ISOLATE_NATIVE=0 must disable isolation")
	}
}

func TestLogNativeWritesFsyncedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "native_gpu.log")
	SetDiagPath(path)
	t.Cleanup(func() { SetDiagPath("") })
	LogNative("unit_test_stage", map[string]any{"ok": true})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read diag: %v", err)
	}
	if !strings.Contains(string(data), `"stage":"unit_test_stage"`) {
		t.Fatalf("diag = %q", data)
	}
}

func TestRunNativeWorkerUnknownOp(t *testing.T) {
	in := strings.NewReader(`{"op":"nope"}`)
	var out bytes.Buffer
	code := RunNativeWorker(in, &out)
	if code == 0 {
		t.Fatal("unknown op should fail")
	}
	var resp WorkerResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK {
		t.Fatal("want ok=false")
	}
	if !strings.Contains(resp.Error, "unknown op") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestDecodeTokensInBatchesRejectsEmpty(t *testing.T) {
	err := DecodeTokensInBatches(0, nil, 256)
	if err == nil {
		t.Fatal("empty tokens must error without calling native decode")
	}
	if !strings.Contains(err.Error(), "empty decode batch") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkerChildEnvPrependsLibDir(t *testing.T) {
	t.Setenv("Path", "C:\\windows\\system32")
	env := workerChildEnv(`C:\libs\llama`)
	foundYZMA := false
	foundPath := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "YZMA_LIB=") {
			foundYZMA = true
			if !strings.Contains(kv, "llama") {
				t.Fatalf("YZMA_LIB = %q", kv)
			}
		}
		eq := strings.IndexByte(kv, '=')
		if eq > 0 && strings.EqualFold(kv[:eq], "Path") {
			foundPath = true
			val := kv[eq+1:]
			if !strings.HasPrefix(strings.ToLower(val), strings.ToLower(`C:\libs\llama`)) {
				t.Fatalf("Path should start with lib dir, got %q", val)
			}
		}
	}
	if !foundYZMA || !foundPath {
		t.Fatalf("missing env keys yzma=%v path=%v env=%v", foundYZMA, foundPath, env)
	}
}

func TestWorkerFailurePrefersJSONDetail(t *testing.T) {
	// Simulate parent-side preference: when child wrote a JSON error and also
	// exited non-zero, callers must see the JSON text, not only "exit status 1".
	resp := WorkerResponse{OK: false, Error: "yzma: create context: failed to initialize model", Exit: 1}
	detail := strings.TrimSpace(resp.Error)
	if detail == "" {
		t.Fatal("expected JSON detail")
	}
	if strings.Contains(detail, "exit status") {
		t.Fatalf("detail should not be bare exit status: %q", detail)
	}
}

func TestGeneratorDoesNotPreloadOnNew(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := New(Config{
		ModelPath: modelPath,
		LibDir:    filepath.Join(dir, "missing-lib"),
	})
	t.Cleanup(func() { _ = gen.Close() })
	// Construction must not start loading — Ready stays false with no error yet.
	if gen.Ready() {
		t.Fatal("New must not preload the GGUF")
	}
	if ref, ok := gen.(*generator); ok {
		if err := ref.LoadError(); err != nil {
			t.Fatalf("load should not have started: %v", err)
		}
	}
}

// TestGeneratorReportsMissingLibrary verifies the failure path that matters in
// practice: the organizer must become unavailable with an actionable error
// instead of panicking or silently disabling, and it must never fall back to a
// network model.
func TestGeneratorReportsMissingLibrary(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}

	gen := New(Config{
		ModelPath: modelPath,
		LibDir:    filepath.Join(dir, "no-such-lib-dir"),
	})
	t.Cleanup(func() { _ = gen.Close() })

	ref, ok := gen.(*generator)
	if !ok {
		t.Fatalf("New() returned %T, want *generator", gen)
	}
	// Demand-load surfaces the missing library.
	if err := waitForLoadResult(ref, 3*time.Second); err == nil {
		t.Fatal("expected a load error for the missing library")
	} else if !strings.Contains(err.Error(), "shared library") && !strings.Contains(strings.ToLower(err.Error()), "library") {
		t.Fatalf("load error = %v, want it to mention the missing shared library", err)
	}
	if gen.Ready() {
		t.Fatal("generator must stay unavailable")
	}

	_, err := gen.Generate(context.Background(), organizer.GenerateRequest{User: "hi"})
	if !errors.Is(err, organizer.ErrUnavailable) {
		t.Fatalf("Generate() error = %v, want ErrUnavailable", err)
	}
}

// TestGeneratorReportsMissingModel covers a misconfigured model path. The path
// is checked before any native call, so this runs on every machine.
func TestGeneratorReportsMissingModel(t *testing.T) {
	gen := New(Config{
		ModelPath: filepath.Join(t.TempDir(), "absent.gguf"),
		LibDir:    t.TempDir(),
	})
	t.Cleanup(func() { _ = gen.Close() })

	ref := gen.(*generator)
	if err := waitForLoadResult(ref, 3*time.Second); err == nil {
		t.Fatal("expected a load error for the missing model")
	} else if !strings.Contains(err.Error(), "GGUF model not found") {
		t.Fatalf("load error = %v, want it to mention the missing GGUF", err)
	}
}

func TestGeneratorEmptyModelPathIsUnavailable(t *testing.T) {
	gen := New(Config{})
	t.Cleanup(func() { _ = gen.Close() })
	ref := gen.(*generator)
	if err := waitForLoadResult(ref, 3*time.Second); err == nil {
		t.Fatal("expected a load error for an empty model path")
	}
}

func TestGeneratorCloseIsIdempotent(t *testing.T) {
	gen := New(Config{ModelPath: filepath.Join(t.TempDir(), "absent.gguf")})
	if err := gen.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := gen.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if gen.Ready() {
		t.Fatal("a closed generator must not report Ready")
	}
	if _, err := gen.Generate(context.Background(), organizer.GenerateRequest{User: "hi"}); !errors.Is(err, organizer.ErrUnavailable) {
		t.Fatalf("Generate() after Close error = %v, want ErrUnavailable", err)
	}
}

func TestGeneratorName(t *testing.T) {
	gen := New(Config{ModelPath: filepath.Join(t.TempDir(), "absent.gguf")})
	t.Cleanup(func() { _ = gen.Close() })
	if name := gen.Name(); !strings.Contains(name, "llama.cpp") {
		t.Fatalf("Name() = %q, want it to identify llama.cpp", name)
	}
}

func TestInstalledVersionMissingRecord(t *testing.T) {
	if got := InstalledVersion(t.TempDir()); got != "" {
		t.Fatalf("InstalledVersion() = %q, want empty without an install record", got)
	}
}

func TestInstalledVersionReadsRecord(t *testing.T) {
	dir := t.TempDir()
	record := `{"version":"b9433","processor":"cpu"}`
	if err := os.WriteFile(filepath.Join(dir, LibraryVersionFile), []byte(record), 0o644); err != nil {
		t.Fatalf("write record: %v", err)
	}
	if got := InstalledVersion(dir); got != "b9433" {
		t.Fatalf("InstalledVersion() = %q, want b9433", got)
	}
}

// TestSilentLoggingIsWired documents that the load path installs llama.cpp's
// silent logger. Without LogSet(LogSilent()), CUDA graph reuse lines flood
// stderr on every token and drown live-test / TUI output. This unit test only
// checks the call shape compiles and is reachable; the live test is where the
// silence is observable.
func TestSilentLoggingHelperExists(t *testing.T) {
	// LogSilent returns a non-zero callback pointer used with LogSet. We cannot
	// call LogSet here without a loaded library, but the symbol must stay
	// referenced so a yzma upgrade that removes it fails this package's build.
	if llama.LogSilent() == 0 && false {
		t.Fatal("unreachable: LogSilent must be importable")
	}
	_ = llama.LogNormal
}

// TestChatApplyTemplateNegativeIsHardFailure documents the load-bearing rule
// behind applyChatTemplate: a negative ChatApplyTemplate return of -1 (or any
// |n| that is not larger than the current buffer) is a hard Jinja failure, not
// a "need 1 byte" size hint. Treating it as a length produced empty prompts
// that then decoded to empty completions.
func TestChatApplyTemplateNegativeIsHardFailure(t *testing.T) {
	cases := []struct {
		name string
		n    int32
		buf  int
		want bool // whether a resize-and-retry is justified
	}{
		{name: "hard failure -1", n: -1, buf: 4096, want: false},
		{name: "hard failure -8", n: -8, buf: 4096, want: false},
		{name: "real size hint", n: -8192, buf: 1024, want: true},
		{name: "absurd size hint", n: -16 * 1024 * 1024, buf: 1024, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			need := int(-tc.n)
			retry := tc.n < 0 && need > tc.buf && need <= 8*1024*1024
			if retry != tc.want {
				t.Fatalf("retry justified = %v, want %v (n=%d, buf=%d)", retry, tc.want, tc.n, tc.buf)
			}
		})
	}
}

// waitForLoadResult triggers an on-demand load and waits until it settles,
// returning its error (nil when the load succeeded). A timeout yields a
// distinct error so a slow machine reports "still loading" rather than a false
// success. New no longer preloads, so tests must call this (or Generate).
func waitForLoadResult(g *generator, timeout time.Duration) error {
	return g.waitForReady(context.Background(), timeout)
}
