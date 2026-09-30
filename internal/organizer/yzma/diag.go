package yzma

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// nativeDiagName is the durable breadcrumb log for llama.cpp / CUDA paths.
// Entries are fsynced so a subsequent access violation still leaves evidence.
const nativeDiagName = "native_gpu.log"

var (
	diagMu   sync.Mutex
	diagPath string
)

// SetDiagPath overrides where native breadcrumbs are written. Empty restores
// the default under ~/.solcode/native_gpu.log.
func SetDiagPath(path string) {
	diagMu.Lock()
	defer diagMu.Unlock()
	diagPath = path
}

func resolveDiagPath() string {
	diagMu.Lock()
	defer diagMu.Unlock()
	if diagPath != "" {
		return diagPath
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nativeDiagName
	}
	return filepath.Join(home, ".solcode", nativeDiagName)
}

// LogNative appends one JSON line and fsyncs. Safe from any goroutine; never
// panics. Use before/after every native load/decode/free so a 0xC0000005 crash
// still leaves the last successful step on disk.
func LogNative(stage string, fields map[string]any) {
	defer func() { _ = recover() }()

	entry := map[string]any{
		"time":  time.Now().Format(time.RFC3339Nano),
		"stage": stage,
		"pid":   os.Getpid(),
	}
	for k, v := range fields {
		entry[k] = v
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := resolveDiagPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		// Fall through; OpenFile may still work for relative paths.
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// Last-resort stderr so interactive runs still see something.
		fmt.Fprintf(os.Stderr, "solcode native diag: %s %s\n", stage, string(data))
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Sync()
	_ = f.Close()
}

// LogNativeErr records a failed native step.
func LogNativeErr(stage string, err error, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	fields["ok"] = false
	LogNative(stage, fields)
}
