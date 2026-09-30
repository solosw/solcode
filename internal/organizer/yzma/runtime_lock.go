package yzma

import "sync"

// runtimeMu serializes every process-wide llama.cpp / ggml backend call.
//
// Organizer generation and GGUF embedding share one CUDA backend. Concurrent
// ModelLoad / Decode / Free from those paths has produced Windows access
// violations (0xC0000005) after successful memory writes. Holding this lock for
// the full native critical section keeps GPU work single-threaded in-process.
var runtimeMu sync.Mutex

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
