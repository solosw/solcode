package jevlocal

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOrtCPUAssetSupportedPlatforms(t *testing.T) {
	cases := []struct {
		goos, goarch string
		wantSuffix   string
	}{
		{"windows", "amd64", ".zip"},
		{"windows", "arm64", ".zip"},
		{"linux", "amd64", ".tgz"},
		{"linux", "arm64", ".tgz"},
	}
	for _, tc := range cases {
		asset, primary, extras, err := ortCPUAsset(tc.goos, tc.goarch)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.goos, tc.goarch, err)
		}
		if asset == "" || primary == "" {
			t.Fatalf("%s/%s empty asset/primary", tc.goos, tc.goarch)
		}
		if !strings.HasSuffix(asset, tc.wantSuffix) {
			t.Fatalf("asset=%q want suffix %q", asset, tc.wantSuffix)
		}
		if len(extras) == 0 {
			t.Fatalf("%s/%s expected extras", tc.goos, tc.goarch)
		}
	}
	if _, _, _, err := ortCPUAsset("darwin", "arm64"); err == nil {
		t.Fatal("expected darwin unsupported")
	}
}

func TestEnsureORTLibraryExplicitMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.dll")
	if _, err := EnsureORTLibrary(missing); err == nil {
		t.Fatal("expected error for missing explicit path")
	}
}

func TestEnsureORTLibraryUsesExisting(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "onnxruntime.dll")
	if err := os.WriteFile(lib, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureORTLibrary(lib)
	if err != nil {
		t.Fatal(err)
	}
	if got != lib {
		t.Fatalf("got %q want %q", got, lib)
	}
}

func TestInstallORTLibraryFromLocalArchive(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("auto-install targets win/linux")
	}
	libDir := t.TempDir()
	dest := filepath.Join(libDir, filepath.Base(DefaultORTLibrary()))

	asset, primaryRel, extras, err := ortCPUAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	pkgName := "onnxruntime-test-" + DefaultORTVersion
	archivePath := filepath.Join(libDir, asset)
	if err := writeFakeORTArchive(archivePath, pkgName, primaryRel, extras); err != nil {
		t.Fatal(err)
	}
	if err := installORTLibrary(dest); err != nil {
		t.Fatalf("installORTLibrary: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	if string(data) != "primary-lib" {
		t.Fatalf("dest content = %q", data)
	}
}

func writeFakeORTArchive(archivePath, pkgName, primaryRel string, extras []string) error {
	files := map[string]string{
		filepath.ToSlash(filepath.Join(pkgName, primaryRel)): "primary-lib",
	}
	for _, rel := range extras {
		files[filepath.ToSlash(filepath.Join(pkgName, rel))] = "extra-lib"
	}
	if strings.HasSuffix(archivePath, ".zip") {
		return writeZipArchive(archivePath, files)
	}
	return writeTarGzArchive(archivePath, files)
}

func writeZipArchive(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			_ = zw.Close()
			return err
		}
		if _, err := io.WriteString(w, body); err != nil {
			_ = zw.Close()
			return err
		}
	}
	return zw.Close()
}

func writeTarGzArchive(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
		if _, err := io.WriteString(tw, body); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

func TestSafeJoinRejectsEscape(t *testing.T) {
	if _, err := safeJoin(t.TempDir(), "../evil"); err == nil {
		t.Fatal("expected escape rejection")
	}
}
