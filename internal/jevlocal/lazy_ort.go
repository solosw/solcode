package jevlocal

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// loadingORTEngine is a non-blocking ORT backend. New returns immediately with
// Ready=false; a background goroutine runs EnsureORTLibrary + NewORTEngine and
// swaps the live session in. Ask/RunNamed keep returning ErrEngineNotReady until
// then (or forever if load fails / Close wins the race).
type loadingORTEngine struct {
	modelPath string
	ortLib    string

	mu     sync.RWMutex
	inner  *ORTEngine
	loadErr error
	closed bool

	done   chan struct{}
	logged atomic.Bool
}

func startLoadingORTEngine(modelPath, ortLib string) *loadingORTEngine {
	e := &loadingORTEngine{
		modelPath: modelPath,
		ortLib:    ortLib,
		done:      make(chan struct{}),
	}
	go e.load()
	return e
}

func (e *loadingORTEngine) load() {
	defer close(e.done)

	lib, err := EnsureORTLibrary(e.ortLib)
	if err != nil {
		e.finish(nil, fmt.Errorf("%w: %v", ErrEngineNotReady, err))
		return
	}
	eng, err := NewORTEngine(ORTOptions{
		ModelPath:     e.modelPath,
		SharedLibrary: lib,
	})
	if err != nil {
		e.finish(nil, fmt.Errorf("%w: %v", ErrEngineNotReady, err))
		return
	}
	e.finish(eng, nil)
}

func (e *loadingORTEngine) finish(eng *ORTEngine, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		if eng != nil {
			_ = eng.Close()
		}
		if e.loadErr == nil {
			e.loadErr = fmt.Errorf("%w: closed before load finished", ErrEngineNotReady)
		}
		return
	}
	e.inner = eng
	e.loadErr = err
}

func (e *loadingORTEngine) Name() string { return EngineORT }

func (e *loadingORTEngine) Ready() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.inner != nil && e.inner.Ready()
}

func (e *loadingORTEngine) RunNamed(ctx context.Context, inputs NamedTensors) (NamedOutputs, error) {
	e.mu.RLock()
	inner := e.inner
	err := e.loadErr
	closed := e.closed
	e.mu.RUnlock()

	if closed {
		return NamedOutputs{}, fmt.Errorf("%w: ort engine closed", ErrEngineNotReady)
	}
	if inner != nil {
		return inner.RunNamed(ctx, inputs)
	}
	if err != nil {
		return NamedOutputs{}, err
	}
	return NamedOutputs{}, fmt.Errorf("%w (ort loading)", ErrEngineNotReady)
}

func (e *loadingORTEngine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		<-e.done
		return nil
	}
	e.closed = true
	inner := e.inner
	e.inner = nil
	e.mu.Unlock()

	<-e.done
	if inner != nil {
		return inner.Close()
	}
	// Load may have finished after we cleared inner under closed=true; finish()
	// already closed that session. Nothing left to destroy.
	return nil
}

// Wait blocks until the background load finishes (success, failure, or Close).
// Intended for tests.
func (e *loadingORTEngine) Wait() {
	if e == nil {
		return
	}
	<-e.done
}

// LoadError returns the background load error once finished, or nil on success /
// still loading.
func (e *loadingORTEngine) LoadError() error {
	if e == nil {
		return nil
	}
	select {
	case <-e.done:
	default:
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.loadErr
}

// notifyLoadOnce invokes fn exactly once after load settles (success or error).
// Used by the app layer to log readiness without spamming.
func (e *loadingORTEngine) notifyLoadOnce(fn func(ready bool, err error)) {
	if e == nil || fn == nil {
		return
	}
	go func() {
		<-e.done
		if !e.logged.CompareAndSwap(false, true) {
			return
		}
		e.mu.RLock()
		ready := e.inner != nil && e.inner.Ready()
		err := e.loadErr
		e.mu.RUnlock()
		fn(ready, err)
	}()
}
