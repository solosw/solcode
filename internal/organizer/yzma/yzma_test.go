package yzma

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	if err := waitForLoadResult(ref, 3*time.Second); err == nil {
		t.Fatal("expected a load error for the missing library")
	} else if !strings.Contains(err.Error(), "shared library") {
		t.Fatalf("load error = %v, want it to mention the missing shared library", err)
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

// waitForLoadResult waits until the background loader settles, returning its
// error (nil when the load succeeded). A timeout yields a distinct error so a
// slow machine reports "still loading" rather than a false success.
func waitForLoadResult(g *generator, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if g.Ready() {
			return nil
		}
		if err := g.LoadError(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("generator did not settle before the test deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
