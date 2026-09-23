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
	// Currently: "ort" loads ONNX Runtime. Empty/"stub" keeps the unimplemented backend.
	EngineName string
	// ORTLib is an optional path to onnxruntime.dll / .so for engine=ort.
	ORTLib string
	// Engine overrides the inference backend. Nil selects from EngineName
	// (or UnimplementedEngine when unset/unavailable).
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
	case "", "stub", "unimplemented":
		return UnimplementedEngine{}, nil
	case EngineORT:
		lib, err := EnsureORTLibrary(opts.ORTLib)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEngineNotReady, err)
		}
		return NewORTEngine(ORTOptions{
			ModelPath:     arts.ONNXPath,
			SharedLibrary: lib,
		})
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
