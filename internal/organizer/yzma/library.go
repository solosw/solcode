// Package yzma loads llama.cpp in-process through purego FFI and implements
// organizer.LocalGenerator.
//
// No CGO is involved and llama.cpp is never compiled here: the shared library is
// resolved at runtime and bound with purego, matching how internal/jevlocal and
// internal/embedding already load ONNX Runtime. Loading a GGUF is slow, so it
// happens on a background goroutine and callers poll Ready rather than blocking
// startup.
package yzma

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/hybridgroup/yzma/pkg/loader"
)

// libraryName is the short name yzma resolves into a platform filename.
const libraryName = "llama"

// ResolveLibraryDir returns the directory holding the llama.cpp shared library.
// An explicit dir wins, then YZMA_LIB, then ~/.solcode/lib/llama.
func ResolveLibraryDir(explicit string) string {
	if dir := strings.TrimSpace(explicit); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("YZMA_LIB")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".solcode", "lib", "llama")
	}
	return filepath.Join(home, ".solcode", "lib", "llama")
}

// LibraryPath returns the platform-specific shared library path inside dir.
func LibraryPath(dir string) string {
	return loader.GetLibraryFilename(strings.TrimSpace(dir), libraryName)
}

// LibraryPresent reports whether the llama.cpp shared library exists in dir.
func LibraryPresent(dir string) bool {
	path := LibraryPath(dir)
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// EnsureRuntime binds llama.cpp and registers ggml backends for dir.
//
// Safe to call from organizer and embedding: llama.Load / backend registration
// are process-global and serialized under the runtime lock. Two different lib
// dirs in one process are not supported.
func EnsureRuntime(dir string) error {
	LockRuntime()
	defer UnlockRuntime()
	return EnsureRuntimeLocked(dir)
}

// EnsureRuntimeLocked is EnsureRuntime for callers that already hold
// LockRuntime. Embedding and organizer use this inside their load paths so the
// whole ModelLoad/InitFromModel sequence stays under one critical section.
func EnsureRuntimeLocked(dir string) error {
	dir = ResolveLibraryDir(dir)
	var bindErr error
	libBindOnce.Do(func() {
		bindErr = loadLibrary(dir)
	})
	if bindErr != nil {
		return bindErr
	}
	if !LibraryPresent(dir) {
		return fmt.Errorf("yzma: llama.cpp shared library not available in %s", LibraryPath(dir))
	}
	backendOnce.Do(func() {
		llama.LogSet(llama.LogSilent())
		llama.BackendInit()
		if err := llama.GGMLBackendLoadAllFromPath(dir); err != nil {
			backendErr = fmt.Errorf("yzma: load ggml backends from %s: %w", dir, err)
		}
	})
	return backendErr
}

// loadLibrary binds the shared library at dir.
//
// llama.Load is process-global: it dlopens once and binds the C symbols. Calling
// it repeatedly for the same directory is harmless, but two different
// directories in one process are not supported by the binding.
func loadLibrary(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("yzma: library directory is empty")
	}
	libPath := LibraryPath(dir)
	if !LibraryPresent(dir) {
		return fmt.Errorf("yzma: llama.cpp shared library not found at %s (install it with `yzma install`, or set memory.organizer.lib_dir / YZMA_LIB)", libPath)
	}
	// Make the side-by-side ggml DLLs resolvable before binding. Without this
	// Windows fails to resolve llama.dll's imports even though every file is
	// present, reporting a missing module that names a dependency.
	if err := prepareLibrarySearchPath(dir); err != nil {
		return fmt.Errorf("yzma: prepare DLL search path for %s: %w", dir, err)
	}
	if err := llama.Load(dir); err != nil {
		return fmt.Errorf("yzma: load llama.cpp from %s: %w", dir, err)
	}
	return nil
}

// ModelPresent reports whether path points at an existing regular file.
func ModelPresent(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// LibraryVersionFile is the install record yzma writes next to the libraries.
// It is read only to report which llama.cpp build is installed.
const LibraryVersionFile = "yzma-install.json"

// InstalledVersion reports the recorded llama.cpp build in dir, or "" when the
// install record is missing. It is advisory: a missing record never blocks use.
func InstalledVersion(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, LibraryVersionFile))
	if err != nil {
		return ""
	}
	// The record is JSON; extract the version without committing to its full
	// schema so a future yzma release cannot break version reporting.
	return extractJSONStringField(string(data), "version")
}

// extractJSONStringField pulls a top-level-ish "key": "value" pair out of JSON
// text. It is intentionally forgiving; reporting a version is best-effort.
func extractJSONStringField(text, key string) string {
	marker := "\"" + key + "\""
	index := strings.Index(text, marker)
	if index < 0 {
		return ""
	}
	rest := text[index+len(marker):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if !strings.HasPrefix(rest, "\"") {
		return ""
	}
	rest = rest[1:]
	end := strings.Index(rest, "\"")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// PlatformLibraryHint describes where the library is expected, for error text.
func PlatformLibraryHint(dir string) string {
	return LibraryPath(ResolveLibraryDir(dir))
}
