package jevlocal

// Int64Tensor is a dense int64 tensor for ORT / onnx-go binding.
type Int64Tensor struct {
	Shape []int64
	Data  []int64
}

// BoolTensor is a dense bool tensor (Laya marker_mask).
type BoolTensor struct {
	Shape []int64
	Data  []bool
}

// Float32Tensor is a dense float32 tensor from model outputs.
type Float32Tensor struct {
	Shape []int64
	Data  []float32
}

// NamedTensors is the family-agnostic session input.
// Keys are ONNX input names (input_ids, attention_mask, seg, marker_pos, …).
type NamedTensors struct {
	Int64 map[string]Int64Tensor
	Bool  map[string]BoolTensor
}

// NamedOutputs is the family-agnostic session output.
type NamedOutputs struct {
	Float32 map[string]Float32Tensor
}

func (t NamedTensors) ensure() NamedTensors {
	if t.Int64 == nil {
		t.Int64 = make(map[string]Int64Tensor)
	}
	if t.Bool == nil {
		t.Bool = make(map[string]BoolTensor)
	}
	return t
}

func (o NamedOutputs) ensure() NamedOutputs {
	if o.Float32 == nil {
		o.Float32 = make(map[string]Float32Tensor)
	}
	return o
}

// flatFloat32 returns the named float32 payload, or nil.
func (o NamedOutputs) flatFloat32(name string) []float32 {
	if o.Float32 == nil {
		return nil
	}
	return o.Float32[name].Data
}
