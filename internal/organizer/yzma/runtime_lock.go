package yzma

import "sync"

// runtimeMu serializes every process-wide llama.cpp / ggml backend call.
//
// Organizer generation and GGUF embedding share one CUDA backend. Concurrent
// ModelLoad / Decode / Free from those paths has produced Windows access
// violations (0xC0000005) after successful memory writes. Holding this lock for
// the full native critical section keeps GPU work single-threaded in-process.
var runtimeMu sync.Mutex

// GPU holders: Windows CUDA backends in this llama.cpp build are unstable when
// two GGUF models stay resident at once (organizer write → embedding index is
// the crash path). Keep at most one GPU-backed model loaded in the process.
var (
	gpuHoldersMu sync.Mutex
	gpuHolders   = map[any]func(){}
)

// WithRuntime runs fn while holding the process-wide llama.cpp lock.
func WithRuntime(fn func()) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	fn()
}

// LockRuntime locks the process-wide llama.cpp runtime. Pair with UnlockRuntime.
// Lock order with per-model mutexes is always: runtimeMu first, then model mu.
func LockRuntime() {
	runtimeMu.Lock()
}

// UnlockRuntime unlocks the process-wide llama.cpp runtime.
func UnlockRuntime() {
	runtimeMu.Unlock()
}

// RegisterGPUHolder records a callback that frees a GPU-backed model.
// unload must only take the holder's own model mutex (not runtimeMu); it is
// invoked while runtimeMu is already held by the loading peer.
func RegisterGPUHolder(id any, unload func()) {
	if id == nil || unload == nil {
		return
	}
	gpuHoldersMu.Lock()
	gpuHolders[id] = unload
	gpuHoldersMu.Unlock()
}

// UnregisterGPUHolder drops a holder after it has freed or closed.
func UnregisterGPUHolder(id any) {
	if id == nil {
		return
	}
	gpuHoldersMu.Lock()
	delete(gpuHolders, id)
	gpuHoldersMu.Unlock()
}

// ReleaseOtherGPUHolders frees every registered GPU model except keep.
// Caller must already hold LockRuntime. Used before loading a new GPU GGUF so
// organizer and embedding never share VRAM/backends in the same process.
func ReleaseOtherGPUHolders(keep any) {
	gpuHoldersMu.Lock()
	callbacks := make([]func(), 0, len(gpuHolders))
	for id, unload := range gpuHolders {
		if id == keep || unload == nil {
			continue
		}
		callbacks = append(callbacks, unload)
	}
	gpuHoldersMu.Unlock()
	for _, unload := range callbacks {
		unload()
	}
}
