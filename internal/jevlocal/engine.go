package jevlocal

import (
	"context"
	"errors"
	"fmt"
)

// ErrEngineNotReady is returned when a local InferenceEngine cannot run yet
// (stub / unimplemented backend). Callers such as Decider treat it like any
// other Ask failure and fall back deterministically.
var ErrEngineNotReady = errors.New("jev local inference engine is not ready")

// TensorBundle is the OpenJev-shaped session input. Shapes follow the model
// card: input_ids/attention_mask/seg are [1, seq], pair_q/pair_opt are [1, pairs].
// Prefer NamedTensors for new code; this type remains for OpenJev helpers/tests.
type TensorBundle struct {
	InputIDs      []int64
	AttentionMask []int64
	Seg           []int64
	PairQ         []int64
	PairOpt       []int64
}

// InferenceEngine runs one forward pass. Implementations may be pure-Go
// (onnx-go), ORT/CGO, CUDA, or a test fake — LocalEvaluator only depends on
// this seam so backends can swap without touching Decider.
type InferenceEngine interface {
	// Name identifies the backend for logs ("unimplemented", "fake", "onnx-go", "ort", …).
	Name() string
	// Ready reports whether RunNamed can succeed. A false value still allows the
	// evaluator to be constructed so Ask fails closed through Decider fallbacks.
	Ready() bool
	// RunNamed executes one forward pass with family-agnostic named tensors.
	RunNamed(ctx context.Context, inputs NamedTensors) (NamedOutputs, error)
	Close() error
}

// UnimplementedEngine is the default production backend until onnx-go / ORT /
// CUDA is wired. Ready is false; RunNamed always returns ErrEngineNotReady.
type UnimplementedEngine struct{}

func (UnimplementedEngine) Name() string { return "unimplemented" }
func (UnimplementedEngine) Ready() bool  { return false }
func (UnimplementedEngine) Close() error { return nil }

func (UnimplementedEngine) RunNamed(context.Context, NamedTensors) (NamedOutputs, error) {
	return NamedOutputs{}, fmt.Errorf("%w: replace with onnx-go or ORT/CUDA backend", ErrEngineNotReady)
}

// FakeEngine returns canned outputs for tests. Ready is true when Outputs or
// Logits is set and Err is nil. Logits is an OpenJev shorthand for a single
// "logits" float32 output.
type FakeEngine struct {
	Outputs NamedOutputs
	Logits  []float32
	Err     error
}

func (f FakeEngine) Name() string { return "fake" }
func (f FakeEngine) Ready() bool {
	return f.Err == nil && (f.Outputs.Float32 != nil || f.Logits != nil)
}
func (f FakeEngine) Close() error { return nil }

func (f FakeEngine) RunNamed(context.Context, NamedTensors) (NamedOutputs, error) {
	if f.Err != nil {
		return NamedOutputs{}, f.Err
	}
	if f.Outputs.Float32 != nil {
		out := NamedOutputs{Float32: make(map[string]Float32Tensor, len(f.Outputs.Float32))}
		for k, v := range f.Outputs.Float32 {
			data := make([]float32, len(v.Data))
			copy(data, v.Data)
			out.Float32[k] = Float32Tensor{Shape: append([]int64(nil), v.Shape...), Data: data}
		}
		return out, nil
	}
	if f.Logits == nil {
		return NamedOutputs{}, ErrEngineNotReady
	}
	data := make([]float32, len(f.Logits))
	copy(data, f.Logits)
	return NamedOutputs{
		Float32: map[string]Float32Tensor{
			"logits": {Shape: []int64{1, int64(len(data))}, Data: data},
		},
	}, nil
}

// namedFromOpenJev lifts an OpenJev TensorBundle into NamedTensors.
func namedFromOpenJev(b TensorBundle) NamedTensors {
	seq := int64(len(b.InputIDs))
	pairs := int64(len(b.PairOpt))
	t := NamedTensors{}.ensure()
	t.Int64["input_ids"] = Int64Tensor{Shape: []int64{1, seq}, Data: append([]int64(nil), b.InputIDs...)}
	t.Int64["attention_mask"] = Int64Tensor{Shape: []int64{1, seq}, Data: append([]int64(nil), b.AttentionMask...)}
	t.Int64["seg"] = Int64Tensor{Shape: []int64{1, seq}, Data: append([]int64(nil), b.Seg...)}
	t.Int64["pair_q"] = Int64Tensor{Shape: []int64{1, pairs}, Data: append([]int64(nil), b.PairQ...)}
	t.Int64["pair_opt"] = Int64Tensor{Shape: []int64{1, pairs}, Data: append([]int64(nil), b.PairOpt...)}
	return t
}

// openJevLogits flattens the primary logits output (OpenJev single-row).
func openJevLogits(outs NamedOutputs) []float32 {
	return outs.flatFloat32("logits")
}
