package bundle

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRuntimeOverrideRequiresNativeExecutable(t *testing.T) {
	dir := t.TempDir()
	binary := "codegraph"
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	path := filepath.Join(dir, binary)
	if err := os.WriteFile(path, []byte("native fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(context.Background(), dir, t.TempDir())
	if err != nil || r.Binary != path {
		t.Fatalf("%+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Resolve(ctx, dir, t.TempDir()); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := Resolve(context.Background(), t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("accepted missing native executable")
	}
}
func TestSourceBuildDoesNotDownloadOrUsePATH(t *testing.T) {
	if _, err := files.ReadFile("assets/runtime.archive"); err == nil {
		t.Skip("embedded runtime present")
	}
	_, err := Resolve(context.Background(), "", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not embedded") {
		t.Fatal(err)
	}
}
func TestPinnedReleaseHasDependencyNotices(t *testing.T) {
	raw, err := files.ReadFile("notices/THIRD_PARTY_NOTICES.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"CodeGraph-Rust v0.55.2", "MIT License", "=== Viewer", "=== tree-sitter"} {
		if !strings.Contains(string(raw), marker) {
			t.Fatal("missing dependency notice", marker)
		}
	}
}

func TestPinnedReleaseCoversSupportedPlatforms(t *testing.T) {
	raw, err := files.ReadFile("release.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"} {
		if !strings.Contains(string(raw), target) {
			t.Fatal("missing", target)
		}
	}
}
