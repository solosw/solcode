package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	for _, marker := range []string{"CodeGraph-Rust v0.55.2", "MIT License", "SolCode modification: solcode-external-index-v1", "=== Viewer", "=== tree-sitter"} {
		if !strings.Contains(string(raw), marker) {
			t.Fatal("missing dependency notice", marker)
		}
	}
}

func TestPatchedPayloadValidation(t *testing.T) {
	raw := []byte("patched native runtime")
	sum := sha256.Sum256(raw)
	var lock release
	lock.Commit = strings.Repeat("a", 40)
	lock.Patch.Revision = "solcode-external-index-v1"
	lock.Patch.SHA256 = strings.Repeat("b", 64)
	valid := payload{Target: "darwin/arm64", SourceCommit: lock.Commit, PatchSHA256: lock.Patch.SHA256, SHA256: hex.EncodeToString(sum[:])}
	if err := validatePayload(lock, valid, raw); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"official archive", "wrong source", "wrong patch", "wrong digest", "corrupt archive", "missing revision"} {
		t.Run(name, func(t *testing.T) {
			target, manifest, archive := valid, lock, raw
			switch name {
			case "official archive":
				target = payload{Target: valid.Target}
			case "wrong source":
				target.SourceCommit = strings.Repeat("c", 40)
			case "wrong patch":
				target.PatchSHA256 = strings.Repeat("c", 64)
			case "wrong digest":
				target.SHA256 = strings.Repeat("c", 64)
			case "corrupt archive":
				archive = []byte("changed executable")
			case "missing revision":
				manifest.Patch.Revision = ""
			}
			if err := validatePayload(manifest, target, archive); err == nil {
				t.Fatal("accepted invalid runtime")
			}
		})
	}
}

func TestPreparedPayload(t *testing.T) {
	raw, err := files.ReadFile("assets/runtime.archive")
	if err != nil {
		t.Skip("runtime not prepared")
	}
	meta, err := files.ReadFile("assets/target.json")
	if err != nil {
		t.Fatal(err)
	}
	var target payload
	if err := json.Unmarshal(meta, &target); err != nil {
		t.Fatal(err)
	}
	meta, err = files.ReadFile("release.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock release
	if err := json.Unmarshal(meta, &lock); err != nil {
		t.Fatal(err)
	}
	if err := validatePayload(lock, target, raw); err != nil {
		t.Fatal(err)
	}
	if target.Target != runtime.GOOS+"/"+runtime.GOARCH {
		t.Skip("prepared for another platform")
	}
	cache := t.TempDir()
	first, err := Resolve(context.Background(), "", cache)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Resolve(context.Background(), "", cache)
	if err != nil || first != again {
		t.Fatalf("cached runtime changed: %+v %+v %v", first, again, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(first.Binary), "THIRD_PARTY_NOTICES.txt")); err != nil {
		t.Fatal(err)
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
