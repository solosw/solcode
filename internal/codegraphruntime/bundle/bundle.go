// Package bundle materializes the pinned native CodeGraph-Rust executable.
// Resolution never installs a runtime or accesses the network.
package bundle

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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

type Runtime struct{ Binary string }

func layout(dir string) (Runtime, error) {
	name := "codegraph"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	r := Runtime{Binary: filepath.Join(dir, name)}
	info, err := os.Lstat(r.Binary)
	if err != nil || !info.Mode().IsRegular() {
		return Runtime{}, fmt.Errorf("CodeGraph native executable missing from %s", dir)
	}
	return r, nil
}

func Resolve(ctx context.Context, override, cache string) (Runtime, error) {
	if err := ctx.Err(); err != nil {
		return Runtime{}, err
	}
	if override != "" {
		dir, err := filepath.Abs(override)
		if err != nil {
			return Runtime{}, err
		}
		return layout(dir)
	}
	raw, err := files.ReadFile("assets/runtime.archive")
	if err != nil {
		return Runtime{}, fmt.Errorf("CodeGraph native runtime not embedded; build with scripts/build-codegraph.sh or configure codegraph.runtime_dir; use Grep/LSP in the meantime")
	}
	var target struct {
		Target string `json:"target"`
	}
	meta, err := files.ReadFile("assets/target.json")
	if err != nil || json.Unmarshal(meta, &target) != nil || target.Target != runtime.GOOS+"/"+runtime.GOARCH {
		return Runtime{}, fmt.Errorf("CodeGraph embedded platform mismatch")
	}
	var lock release
	meta, _ = files.ReadFile("release.json")
	if err := json.Unmarshal(meta, &lock); err != nil {
		return Runtime{}, err
	}
	asset, ok := lock.Assets[target.Target]
	if !ok {
		return Runtime{}, fmt.Errorf("unsupported CodeGraph platform %s", target.Target)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != asset.SHA256 {
		return Runtime{}, fmt.Errorf("CodeGraph archive SHA256 mismatch")
	}
	notices, err := files.ReadFile("notices/THIRD_PARTY_NOTICES.txt")
	if err != nil {
		return Runtime{}, fmt.Errorf("CodeGraph notices missing: %w", err)
	}
	noticeHash := sha256.Sum256(notices)
	dir := filepath.Join(cache, lock.Version, runtime.GOOS+"-"+runtime.GOARCH, asset.SHA256+"-"+hex.EncodeToString(noticeHash[:8]))
	installMu.Lock()
	defer installMu.Unlock()
	if marker, err := os.ReadFile(filepath.Join(dir, ".complete")); err == nil && string(marker) == asset.SHA256 {
		if r, err := layout(dir); err == nil {
			return r, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return Runtime{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return Runtime{}, err
	}
	temp, err := os.MkdirTemp(filepath.Dir(dir), ".extract-")
	if err != nil {
		return Runtime{}, err
	}
	defer os.RemoveAll(temp)
	if err := extract(ctx, raw, strings.HasSuffix(asset.Name, ".zip"), temp); err != nil {
		return Runtime{}, fmt.Errorf("extract CodeGraph: %w", err)
	}
	r, err := layout(temp)
	if err != nil {
		return Runtime{}, err
	}
	if err := os.Chmod(r.Binary, 0755); err != nil {
		return Runtime{}, err
	}
	if err := os.WriteFile(filepath.Join(temp, "THIRD_PARTY_NOTICES.txt"), notices, 0644); err != nil {
		return Runtime{}, err
	}
	if err := os.WriteFile(filepath.Join(temp, ".complete"), []byte(asset.SHA256), 0600); err != nil {
		return Runtime{}, err
	}
	if err := os.Rename(temp, dir); err != nil {
		marker, readErr := os.ReadFile(filepath.Join(dir, ".complete"))
		if readErr != nil || string(marker) != asset.SHA256 {
			return Runtime{}, fmt.Errorf("install CodeGraph runtime: %w", err)
		}
	}
	return layout(dir)
}
