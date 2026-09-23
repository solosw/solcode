package jevlocal

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/httpproxy"
)

// DefaultORTVersion is the pinned ONNX Runtime release used for auto-install.
// Keep in sync with a shared library known to work with github.com/yalue/onnxruntime_go.
const DefaultORTVersion = "1.30.0"

const ortReleaseBase = "https://github.com/microsoft/onnxruntime/releases/download"

// EnsureORTLibrary returns a usable onnxruntime shared-library path.
// If explicit is empty and the default ~/.solcode/lib copy is missing, it
// downloads the CPU package for the current GOOS/GOARCH and installs the
// primary library (plus providers_shared when present) under ~/.solcode/lib.
func EnsureORTLibrary(explicit string) (string, error) {
	dest := ResolveORTLibrary(explicit)
	if ORTLibraryAvailable(explicit) {
		return dest, nil
	}
	if strings.TrimSpace(explicit) != "" {
		return "", fmt.Errorf("onnxruntime library not found at %s", dest)
	}
	if err := installORTLibrary(dest); err != nil {
		return "", err
	}
	if !ORTLibraryAvailable("") {
		return "", fmt.Errorf("onnxruntime install finished but %s is still missing", dest)
	}
	return dest, nil
}

func installORTLibrary(dest string) error {
	asset, libRel, extras, err := ortCPUAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	libDir := filepath.Dir(dest)
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return fmt.Errorf("create ort lib dir: %w", err)
	}

	archivePath := filepath.Join(libDir, asset)
	url := fmt.Sprintf("%s/v%s/%s", ortReleaseBase, DefaultORTVersion, asset)
	if err := downloadFile(url, archivePath); err != nil {
		return err
	}

	extractRoot := filepath.Join(libDir, strings.TrimSuffix(strings.TrimSuffix(asset, ".zip"), ".tgz"))
	_ = os.RemoveAll(extractRoot)
	if err := os.MkdirAll(extractRoot, 0o755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}
	if strings.HasSuffix(asset, ".zip") {
		if err := extractZip(archivePath, extractRoot); err != nil {
			return err
		}
	} else {
		if err := extractTarGz(archivePath, extractRoot); err != nil {
			return err
		}
	}

	srcRoot, err := findORTPackageRoot(extractRoot)
	if err != nil {
		return err
	}
	if err := copyFile(filepath.Join(srcRoot, libRel), dest); err != nil {
		return fmt.Errorf("install %s: %w", filepath.Base(dest), err)
	}
	for _, rel := range extras {
		from := filepath.Join(srcRoot, rel)
		to := filepath.Join(libDir, filepath.Base(rel))
		if err := copyFile(from, to); err != nil {
			// providers_shared is optional for CPU-only loads but copy when present.
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("install %s: %w", filepath.Base(to), err)
		}
	}
	return nil
}

func ortCPUAsset(goos, goarch string) (asset, primaryRel string, extras []string, err error) {
	switch goos {
	case "windows":
		switch goarch {
		case "amd64":
			return fmt.Sprintf("onnxruntime-win-x64-%s.zip", DefaultORTVersion),
				filepath.Join("lib", "onnxruntime.dll"),
				[]string{filepath.Join("lib", "onnxruntime_providers_shared.dll")},
				nil
		case "arm64":
			return fmt.Sprintf("onnxruntime-win-arm64-%s.zip", DefaultORTVersion),
				filepath.Join("lib", "onnxruntime.dll"),
				[]string{filepath.Join("lib", "onnxruntime_providers_shared.dll")},
				nil
		}
	case "linux":
		switch goarch {
		case "amd64":
			so := fmt.Sprintf("libonnxruntime.so.%s", DefaultORTVersion)
			return fmt.Sprintf("onnxruntime-linux-x64-%s.tgz", DefaultORTVersion),
				filepath.Join("lib", so),
				[]string{filepath.Join("lib", "libonnxruntime_providers_shared.so")},
				nil
		case "arm64":
			so := fmt.Sprintf("libonnxruntime.so.%s", DefaultORTVersion)
			return fmt.Sprintf("onnxruntime-linux-aarch64-%s.tgz", DefaultORTVersion),
				filepath.Join("lib", so),
				[]string{filepath.Join("lib", "libonnxruntime_providers_shared.so")},
				nil
		}
	}
	return "", "", nil, fmt.Errorf("no onnxruntime auto-install package for %s/%s", goos, goarch)
}

func downloadFile(url, dest string) error {
	if info, err := os.Stat(dest); err == nil && !info.IsDir() && info.Size() > 0 {
		return nil
	}
	tmp := dest + ".partial"
	_ = os.Remove(tmp)
	client := httpproxy.NewClient(10 * time.Minute)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "solcode/"+DefaultORTVersion)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", url, resp.Status)
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, dest)
}

func extractZip(archive, dest string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if err := writeZipEntry(dest, f); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(dest string, f *zip.File) error {
	target, err := safeJoin(dest, f.Name)
	if err != nil {
		return err
	}
	if f.FileInfo().IsDir() {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, rc)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func findORTPackageRoot(extractRoot string) (string, error) {
	entries, err := os.ReadDir(extractRoot)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "onnxruntime-") {
			return filepath.Join(extractRoot, e.Name()), nil
		}
	}
	// Some archives unpack with lib/ at the top.
	if _, err := os.Stat(filepath.Join(extractRoot, "lib")); err == nil {
		return extractRoot, nil
	}
	return "", fmt.Errorf("onnxruntime package root not found under %s", extractRoot)
}

func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing path escape in archive entry %q", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("refusing path escape in archive entry %q", name)
	}
	return target, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}
