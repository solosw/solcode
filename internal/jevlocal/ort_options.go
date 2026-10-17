package jevlocal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	EngineORT           = "ort"
	defaultORTLibName   = "onnxruntime.dll"
	defaultORTLibNameSO = "libonnxruntime.so"
)

// ORTOptions configures an ONNX Runtime session.
type ORTOptions struct {
	// ModelPath is the .onnx graph (external .onnx_data may sit beside it).
	ModelPath string
	// SharedLibrary is the onnxruntime.dll / .so path. Empty uses
	// ~/.solcode/lib/onnxruntime.dll (Windows) or libonnxruntime.so.
	SharedLibrary string
	// GPU enables the CUDA execution provider. Requires a CUDA-capable ORT
	// shared library (the auto-installed package is CPU-only).
	GPU bool
	// CudaDeviceID selects the CUDA device when GPU is true (default 0).
	CudaDeviceID int
}

// DefaultORTLibrary returns the default shared-library path under ~/.solcode/lib.
func DefaultORTLibrary() string {
	dir := filepath.Join(userConfigDir(), "lib")
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(dir, defaultORTLibName)
	case "linux":
		// yalue/onnxruntime_go needs the versioned .so, not the unversioned symlink.
		return filepath.Join(dir, "libonnxruntime.so."+DefaultORTVersion)
	default:
		return filepath.Join(dir, defaultORTLibNameSO)
	}
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}

func userConfigDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".solcode")
	}
	return filepath.Join(".", ".solcode")
}

// ResolveORTLibrary picks an explicit path or the default under ~/.solcode/lib.
func ResolveORTLibrary(explicit string) string {
	if p := strings.TrimSpace(explicit); p != "" {
		return p
	}
	return DefaultORTLibrary()
}

// ORTLibraryAvailable reports whether the shared library file exists.
func ORTLibraryAvailable(explicit string) bool {
	p := ResolveORTLibrary(explicit)
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
