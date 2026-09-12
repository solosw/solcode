package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashBytesStable(t *testing.T) {
	a := HashBytes([]byte("hello"))
	b := HashBytes([]byte("hello"))
	c := HashBytes([]byte("world"))
	if a == "" || a != b {
		t.Fatalf("hash unstable: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("different content produced same hash")
	}
}

func TestSnapshotAndDiffFingerprints(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "x.js"), []byte("skip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := SnapshotWorkDir(root, FingerprintOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before["a.txt"]; !ok {
		t.Fatalf("missing a.txt in %#v", before)
	}
	if _, ok := before["node_modules/x.js"]; ok {
		t.Fatal("node_modules should be skipped")
	}
	if _, ok := before["bin.dat"]; ok {
		t.Fatal("binary should be skipped")
	}

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	cPath := filepath.Join(root, "c.txt")
	if err := os.WriteFile(cPath, []byte("soon-gone"), 0o644); err != nil {
		t.Fatal(err)
	}
	mid, err := SnapshotWorkDir(root, FingerprintOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cPath); err != nil {
		t.Fatal(err)
	}
	after, err := SnapshotWorkDir(root, FingerprintOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}

	changes := DiffFingerprints(before, mid)
	byPath := map[string]*string{}
	for _, ch := range changes {
		byPath[ch.Path] = ch.Content
	}
	if content, ok := byPath["a.txt"]; !ok || content == nil || *content != "v1" {
		t.Fatalf("a.txt change = %v ok=%v", content, ok)
	}
	if content, ok := byPath["b.txt"]; !ok || content != nil {
		t.Fatalf("b.txt should be created (nil content), got %v ok=%v", content, ok)
	}

	del := DiffFingerprints(mid, after)
	foundDel := false
	for _, ch := range del {
		if ch.Path == "c.txt" {
			foundDel = true
			if ch.Content == nil || *ch.Content != "soon-gone" {
				t.Fatalf("c.txt delete content = %#v", ch.Content)
			}
		}
	}
	if !foundDel {
		t.Fatalf("expected c.txt delete in %#v", del)
	}
}

func TestFingerprintCheckpointToolBashOnly(t *testing.T) {
	if !FingerprintCheckpointTool(BashToolName) {
		t.Fatal("Bash should use fingerprint checkpoints")
	}
	if FingerprintCheckpointTool(WriteToolName) {
		t.Fatal("Write should stay on path-based capture")
	}
	if CheckpointableFileTool(BashToolName) {
		t.Fatal("Bash must not be path-checkpointable")
	}
}
