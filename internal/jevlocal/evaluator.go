package jevlocal

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/solosw/solcode/internal/systemone"
)

// Options configures a local Evaluator.
type Options struct {
	ModelDir string
	Model    string
	DType    string
	// EngineName selects a built-in InferenceEngine when Engine is nil.
	// Empty and "ort" start ONNX Runtime asynchronously (New returns before
	// the session is Ready). "stub"/"unimplemented" keep the unimplemented
	// backend so Ask fails into Decider fallbacks.
	EngineName string
	// ORTLib is an optional path to onnxruntime.dll / .so for engine=ort.
	ORTLib string
	// Engine overrides the inference backend. Nil selects from EngineName
	// (or UnimplementedEngine when stub/unavailable). Injected engines are
	// used as-is and are not wrapped in the async ORT loader.
	Engine InferenceEngine
}

// LocalEvaluator implements systemone.Evaluator for on-disk OpenJev/Laya artifacts.
type LocalEvaluator struct {
	artifacts Artifacts
	engine    InferenceEngine
	family    ModelFamily

	mu    sync.Mutex
	cache map[string]systemone.Answers
}

// New builds a LocalEvaluator after validating model_dir artifacts.
func New(opts Options) (*LocalEvaluator, error) {
	arts, err := ResolveArtifacts(opts.ModelDir, opts.DType, opts.Model)
	if err != nil {
		return nil, err
	}
	engine := opts.Engine
	if engine == nil {
		engine, err = defaultEngine(arts, opts)
		if err != nil {
			return nil, err
		}
	}
	family, err := newFamily(arts)
	if err != nil {
		_ = engine.Close()
		return nil, err
	}
	return &LocalEvaluator{
		artifacts: arts,
		engine:    engine,
		family:    family,
		cache:     make(map[string]systemone.Answers),
	}, nil
}

func defaultEngine(arts Artifacts, opts Options) (InferenceEngine, error) {
	switch strings.ToLower(strings.TrimSpace(opts.EngineName)) {
	case "stub", "unimplemented":
		return UnimplementedEngine{}, nil
	case "", EngineORT:
		// Async: return immediately so app.New / ReloadFeatures are not blocked
		// by ORT shared-lib install + session create. Ask falls back until Ready.
		return startLoadingORTEngine(arts.ONNXPath, opts.ORTLib), nil
	case "onnx-go":
		// Reserved: pure-Go backend not wired yet (onnx-go/gorgonnx cannot load
		// OpenJev INT4 or reliably run Laya). Prefer engine=ort with auto-install.
		return nil, fmt.Errorf("%w: engine onnx-go is not implemented yet", ErrEngineNotReady)
	default:
		return nil, fmt.Errorf("unknown local jev engine %q", opts.EngineName)
	}
}

// Compile-time check.
var _ systemone.Evaluator = (*LocalEvaluator)(nil)

func (e *LocalEvaluator) Backend() string { return systemone.BackendLocal }
func (e *LocalEvaluator) Model() string {
	if e == nil {
		return ""
	}
	return e.artifacts.ModelID
}
func (e *LocalEvaluator) Configured() bool {
	return e != nil && e.engine != nil && e.family != nil
}
func (e *LocalEvaluator) Family() string {
	if e == nil {
		return ""
	}
	return e.artifacts.Family
}
func (e *LocalEvaluator) Artifacts() Artifacts {
	if e == nil {
		return Artifacts{}
	}
	return e.artifacts
}
func (e *LocalEvaluator) EngineName() string {
	if e == nil || e.engine == nil {
		return ""
	}
	return e.engine.Name()
}

// EngineReady reports whether the underlying InferenceEngine can RunNamed.
func (e *LocalEvaluator) EngineReady() bool {
	if e == nil || e.engine == nil {
		return false
	}
	return e.engine.Ready()
}

// WaitEngine blocks until an async ORT load settles (or returns immediately for
// sync engines). Intended for tests and optional readiness logging.
func (e *LocalEvaluator) WaitEngine() {
	if e == nil {
		return
	}
	if loader, ok := e.engine.(*loadingORTEngine); ok {
		loader.Wait()
	}
}

// OnEngineReady invokes fn once after an async ORT load settles. Sync engines
// call fn immediately on a new goroutine.
func (e *LocalEvaluator) OnEngineReady(fn func(ready bool, err error)) {
	if e == nil || fn == nil {
		return
	}
	if loader, ok := e.engine.(*loadingORTEngine); ok {
		loader.notifyLoadOnce(fn)
		return
	}
	go fn(e.EngineReady(), nil)
}

// Close releases the underlying engine.
func (e *LocalEvaluator) Close() error {
	if e == nil || e.engine == nil {
		return nil
	}
	return e.engine.Close()
}

// Ask evaluates questions through the family packer + InferenceEngine.
// Without a ready engine it returns ErrEngineNotReady so Decider falls back.
func (e *LocalEvaluator) Ask(ctx context.Context, state any, questions map[string]systemone.Question) (systemone.Answers, systemone.Usage, error) {
	if e == nil {
		return nil, systemone.Usage{}, fmt.Errorf("jev local evaluator is nil")
	}
	if len(questions) == 0 {
		return systemone.Answers{}, systemone.Usage{}, nil
	}
	if e.engine == nil || !e.engine.Ready() {
		if loader, ok := e.engine.(*loadingORTEngine); ok {
			if err := loader.LoadError(); err != nil {
				return nil, systemone.Usage{}, err
			}
			return nil, systemone.Usage{}, fmt.Errorf("%w (ort loading)", ErrEngineNotReady)
		}
		return nil, systemone.Usage{}, fmt.Errorf("%w (%s)", ErrEngineNotReady, e.EngineName())
	}
	if e.family == nil {
		return nil, systemone.Usage{}, fmt.Errorf("%w: model family not loaded", ErrEngineNotReady)
	}

	inputs, kept, err := e.family.Build(ctx, state, questions)
	if err != nil {
		return nil, systemone.Usage{}, err
	}
	outs, err := e.engine.RunNamed(ctx, inputs)
	if err != nil {
		return nil, systemone.Usage{}, err
	}
	answers, err := e.family.Decode(kept, outs)
	if err != nil {
		return nil, systemone.Usage{}, err
	}
	return answers, systemone.Usage{}, nil
}
