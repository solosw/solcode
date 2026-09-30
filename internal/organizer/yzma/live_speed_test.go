//go:build live

package yzma

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/organizer"
)

// TestLiveQwenSpeedProbe times load / generate / release on the developer's
// small organizer model. Opt-in via -tags live.
//
// llama.cpp cannot do autoregressive generation with literally no KV cache.
// This probe measures the practical solcode path instead:
//
//	load → Generate (MemoryClear at start) → free model after each call
//
// versus keeping the model resident between calls.
func TestLiveQwenSpeedProbe(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	modelPath := os.Getenv("SOLCODE_ORGANIZER_MODEL")
	if modelPath == "" {
		modelPath = filepath.Join(home, ".solcode", "models", "Qwen3.5-0.8B-Q5_K_S.gguf")
	}
	libDir := os.Getenv("YZMA_LIB")
	if libDir == "" {
		libDir = filepath.Join(home, ".solcode", "lib", "llama")
	}
	if !ModelPresent(modelPath) {
		t.Skipf("model not found: %s", modelPath)
	}
	if !LibraryPresent(libDir) {
		t.Skipf("llama lib not found: %s", libDir)
	}

	gpuLayers := 0
	if v := os.Getenv("SOLCODE_ORGANIZER_GPU_LAYERS"); v != "" {
		fmt.Sscanf(v, "%d", &gpuLayers)
	}
	t.Logf("model=%s lib=%s gpu_layers=%d", modelPath, libDir, gpuLayers)

	var ms runtime.MemStats
	readRSS := func(label string) {
		runtime.GC()
		runtime.ReadMemStats(&ms)
		t.Logf("mem %-22s heap_alloc=%6.1fMiB sys=%6.1fMiB",
			label, float64(ms.HeapAlloc)/1024/1024, float64(ms.Sys)/1024/1024)
	}

	req := organizer.GenerateRequest{
		System:      "Return ONLY one short XML document rooted at <result> with <session_summary>, <keywords>, <importance>, empty <candidate_memories>.",
		User:        `{"session_id":"bench","transcript":"user: wire organizer\nassistant: done\nuser: run tests\nassistant: passed"}`,
		MaxTokens:   128,
		Temperature: 0,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Path A: release after each Generate (default multi-workspace path).
	genA := New(Config{
		ModelPath:       modelPath,
		LibDir:          libDir,
		ContextSize:     16384,
		GPULayers:       gpuLayers,
		IdleUnloadAfter: 30 * time.Second,
	}).(*generator)
	t.Cleanup(func() { _ = genA.Close() })

	readRSS("A_before")
	t1 := time.Now()
	out1, err := genA.Generate(ctx, req)
	d1 := time.Since(t1)
	t.Logf("A_gen1_cold load+decode=%s out_bytes=%d ready_after=%v err=%v preview=%q",
		d1.Round(time.Millisecond), len(out1), genA.Ready(), err, trimForLog(out1, 160))
	if err != nil {
		t.Fatalf("A Generate#1: %v", err)
	}
	readRSS("A_after_gen1_released")

	t2 := time.Now()
	out2, err := genA.Generate(ctx, req)
	d2 := time.Since(t2)
	t.Logf("A_gen2_reload+decode=%s out_bytes=%d ready_after=%v err=%v",
		d2.Round(time.Millisecond), len(out2), genA.Ready(), err)
	if err != nil {
		t.Fatalf("A Generate#2: %v", err)
	}
	readRSS("A_after_gen2_released")

	// Path B: keep resident; only MemoryClear between calls.
	genB := New(Config{
		ModelPath:       modelPath,
		LibDir:          libDir,
		ContextSize:     16384,
		GPULayers:       gpuLayers,
		IdleUnloadAfter: -1,
	}).(*generator)
	t.Cleanup(func() { _ = genB.Close() })

	tLoadB := time.Now()
	if err := genB.waitForReady(context.Background(), 3*time.Minute); err != nil {
		t.Fatalf("B load: %v", err)
	}
	loadB := time.Since(tLoadB)
	t.Logf("B_load_only=%s", loadB.Round(time.Millisecond))
	readRSS("B_after_load_resident")

	if _, err := genB.Generate(ctx, req); err != nil {
		t.Fatalf("B warmup: %v", err)
	}
	t3 := time.Now()
	out3, err := genB.Generate(ctx, req)
	d3 := time.Since(t3)
	t.Logf("B_gen_resident_only=%s out_bytes=%d err=%v preview=%q",
		d3.Round(time.Millisecond), len(out3), err, trimForLog(out3, 160))
	if err != nil {
		t.Fatalf("B Generate: %v", err)
	}
	readRSS("B_after_resident_gen")

	t.Logf("SUMMARY cold_load+gen=%s reload+gen=%s resident_gen_only=%s (llama.cpp has no no-KV mode; resident path is MemoryClear-per-call)",
		d1.Round(time.Millisecond), d2.Round(time.Millisecond), d3.Round(time.Millisecond))
}
