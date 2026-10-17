// Package bundle materializes the pinned open-computer-use native runtime.
// Source builds can instead configure computer_use.binary or use PATH.
package bundle

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

//go:embed release.json assets/* notices/*
var files embed.FS

var installMu sync.Mutex

type release struct {
	Version string `json:"version"`
	Assets  map[string]struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"assets"`
}

// Resolve does no network I/O. Explicit paths take precedence over embedded
// payloads; PATH is only consulted when this is a source build without payload.
func Resolve(ctx context.Context, binary, cacheDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if binary = strings.TrimSpace(binary); binary != "" {
		resolved, err := exec.LookPath(binary)
		if err != nil {
			return "", fmt.Errorf("computer_use.binary: %w", err)
		}
		return resolved, nil
	}
	data, err := files.ReadFile("assets/runtime.archive")
	if err != nil {
		if binary, err := exec.LookPath("open-computer-use"); err == nil {
			return binary, nil
		}
		return "", fmt.Errorf("ComputerUse runtime is not embedded; build with scripts/build-computeruse.sh or set computer_use.binary to the native open-computer-use executable (legacy robotgo is disabled)")
	}
	var target struct {
		Target string `json:"target"`
	}
	raw, err := files.ReadFile("assets/target.json")
	if err != nil || json.Unmarshal(raw, &target) != nil {
		return "", fmt.Errorf("invalid embedded ComputerUse target metadata")
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	if target.Target != platform {
		return "", fmt.Errorf("embedded ComputerUse runtime is for %s, not %s; rebuild for this platform", target.Target, platform)
	}
	var lock release
	raw, _ = files.ReadFile("release.json")
	if err := json.Unmarshal(raw, &lock); err != nil {
		return "", err
	}
	asset, ok := lock.Assets[platform]
	if !ok {
		return "", fmt.Errorf("unsupported ComputerUse platform %s", platform)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != asset.SHA256 {
		return "", fmt.Errorf("embedded ComputerUse SHA256 mismatch")
	}
	relativeBinary := "open-computer-use"
	switch runtime.GOOS {
	case "darwin":
		relativeBinary = "Open Computer Use.app/Contents/MacOS/OpenComputerUse"
	case "windows":
		relativeBinary += ".exe"
	}
	installMu.Lock()
	defer installMu.Unlock()
	dir := filepath.Join(cacheDir, lock.Version, runtime.GOOS+"-"+runtime.GOARCH, asset.SHA256)
	executable := filepath.Join(dir, filepath.FromSlash(relativeBinary))
	if installed(dir, executable) {
		return executable, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(filepath.Dir(dir), ".extract-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	if err := extract(ctx, data, strings.HasSuffix(asset.Name, ".zip"), temp); err != nil {
		return "", fmt.Errorf("extract ComputerUse: %w", err)
	}
	if info, err := os.Lstat(filepath.Join(temp, filepath.FromSlash(relativeBinary))); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("ComputerUse executable missing from embedded archive")
	}
	if err := os.Chmod(filepath.Join(temp, filepath.FromSlash(relativeBinary)), 0o755); err != nil {
		return "", err
	}
	for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES.md"} {
		notice, err := files.ReadFile("notices/" + name)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(temp, "OPEN_COMPUTER_USE_"+name), notice, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(temp, ".complete"), []byte(asset.SHA256), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temp, dir); err != nil {
		// Another SolCode process may have finished the same atomic extraction.
		if !installed(dir, executable) {
			return "", fmt.Errorf("install ComputerUse runtime: %w", err)
		}
	}
	return executable, nil
}

func installed(dir, executable string) bool {
	if marker, err := os.ReadFile(filepath.Join(dir, ".complete")); err != nil || string(marker) != filepath.Base(dir) {
		return false
	}
	info, err := os.Stat(executable)
	return err == nil && info.Mode().IsRegular()
}
