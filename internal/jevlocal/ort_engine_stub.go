//go:build !cgo

package jevlocal

import (
	"context"
	"fmt"
)

// The current app uses GGUF for local models. Keep legacy ORT configuration
// parseable in pure-Go release builds without pulling in the C-only ORT binding.
// Builds with CGO retain the complete implementation in ort_engine.go.
type ORTEngine struct{}

func NewORTEngine(ORTOptions) (*ORTEngine, error) {
	return nil, fmt.Errorf("%w: legacy ONNX Runtime inference requires a CGO-enabled build; use the GGUF backend in pure-Go builds", ErrEngineNotReady)
}
func (*ORTEngine) Name() string { return EngineORT }
func (*ORTEngine) Ready() bool  { return false }
func (*ORTEngine) Close() error { return nil }
func (*ORTEngine) RunNamed(context.Context, NamedTensors) (NamedOutputs, error) {
	return NamedOutputs{}, fmt.Errorf("%w: ONNX Runtime requires CGO", ErrEngineNotReady)
}

func (e *ORTEngine) Run(ctx context.Context, inputs TensorBundle) ([]float32, error) {
	_, err := e.RunNamed(ctx, namedFromOpenJev(inputs))
	return nil, err
}
