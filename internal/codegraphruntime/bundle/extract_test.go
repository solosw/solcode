package bundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"runtime"

	"testing"
)

type archiveEntry struct {
	name, text string
	mode       os.FileMode
}

func zipBytes(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(e.text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestExtractCodeGraphArchivePreservesExecutable(t *testing.T) {
	dir := t.TempDir()
	data := zipBytes(t, archiveEntry{"App.app/Contents/MacOS/Main", "main", 0755}, archiveEntry{"App.app/Contents/Resources/helper", "helper", 0755})
	if err := extract(context.Background(), data, true, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"App.app/Contents/MacOS/Main", "App.app/Contents/Resources/helper"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
			t.Fatal("executable bit lost")
		}
	}
}
func TestExtractRejectsUnsafePathsAndLinks(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/drive", `dir\escape`} {
		t.Run(name, func(t *testing.T) {
			if err := extract(context.Background(), zipBytes(t, archiveEntry{name, "bad", 0644}), true, t.TempDir()); err == nil {
				t.Fatal("accepted unsafe path")
			}
		})
	}
	for _, target := range []string{"../../../escape", "/absolute", `C:\escape`} {
		t.Run(target, func(t *testing.T) {
			if err := extract(context.Background(), zipBytes(t, archiveEntry{"dir/link", target, os.ModeSymlink | 0777}), true, t.TempDir()); err == nil {
				t.Fatal("accepted unsafe link")
			}
		})
	}
	// Delaying symlink creation prevents a write through a link even when the
	// link points to an otherwise valid in-archive directory.
	data := zipBytes(t, archiveEntry{"dir/link", "../target", os.ModeSymlink | 0777}, archiveEntry{"dir/link/file", "bad", 0644})
	if err := extract(context.Background(), data, true, t.TempDir()); err == nil {
		t.Fatal("accepted write through archive symlink")
	}
}
func TestExtractArchiveSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires Windows developer mode")
	}
	dir := t.TempDir()
	data := zipBytes(t, archiveEntry{"dir/file", "contents", 0644}, archiveEntry{"dir/link", "file", os.ModeSymlink | 0777})
	if err := extract(context.Background(), data, true, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dir", "link"))
	if err != nil || string(got) != "contents" {
		t.Fatalf("got=%s err=%v", got, err)
	}
}
func TestExtractTarAndCancelledContext(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	text := "native"
	if err := tw.WriteHeader(&tar.Header{Name: "codegraph", Mode: 0755, Size: int64(len(text))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(text))
	_ = tw.Close()
	_ = gz.Close()
	dir := t.TempDir()
	if err := extract(context.Background(), buf.Bytes(), false, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "codegraph"))
	if err != nil || string(got) != text {
		t.Fatalf("got=%s err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := extract(ctx, buf.Bytes(), false, t.TempDir()); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestExtractRejectsChainedSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege")
	}
	data := zipBytes(t,
		archiveEntry{"dir/a", "../b", os.ModeSymlink | 0777},
		archiveEntry{"b", "dir/a/../../escape", os.ModeSymlink | 0777},
	)
	if err := extract(context.Background(), data, true, t.TempDir()); err == nil {
		t.Fatal("accepted escaping/cyclic chain")
	}
}
