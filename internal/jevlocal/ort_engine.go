package jevlocal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	EngineORT           = "ort"
	defaultORTLibName   = "onnxruntime.dll"
	defaultORTLibNameSO = "libonnxruntime.so"
)

// ORTOptions configures an ONNX Runtime session.
type ORTOptions struct {
	// ModelPath is the .onnx graph (external .onnx_data may sit beside it).
	ModelPath string
	// SharedLibrary is the onnxruntime.dll / .so path. Empty uses
	// ~/.solcode/lib/onnxruntime.dll (Windows) or libonnxruntime.so.
	SharedLibrary string
	// GPU enables the CUDA execution provider. Requires a CUDA-capable ORT
	// shared library (the auto-installed package is CPU-only).
	GPU bool
	// CudaDeviceID selects the CUDA device when GPU is true (default 0).
	CudaDeviceID int
}

var (
	ortEnvMu   sync.Mutex
	ortEnvOnce sync.Once
	ortEnvErr  error
	ortLibUsed string
)

// DefaultORTLibrary returns the default shared-library path under ~/.solcode/lib.
func DefaultORTLibrary() string {
	dir := filepath.Join(userConfigDir(), "lib")
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(dir, defaultORTLibName)
	case "linux":
		// yalue/onnxruntime_go needs the versioned .so, not the unversioned symlink.
		return filepath.Join(dir, "libonnxruntime.so."+DefaultORTVersion)
	default:
		return filepath.Join(dir, defaultORTLibNameSO)
	}
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}

func userConfigDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".solcode")
	}
	return filepath.Join(".", ".solcode")
}

func ensureORTEnvironment(sharedLibrary string) error {
	ortEnvOnce.Do(func() {
		lib := strings.TrimSpace(sharedLibrary)
		if lib == "" {
			lib = DefaultORTLibrary()
		}
		if _, err := os.Stat(lib); err != nil {
			ortEnvErr = fmt.Errorf("onnxruntime shared library: %w", err)
			return
		}
		ort.SetSharedLibraryPath(lib)
		if err := ort.InitializeEnvironment(); err != nil {
			ortEnvErr = fmt.Errorf("InitializeEnvironment: %w", err)
			return
		}
		ortLibUsed = lib
	})
	return ortEnvErr
}

// ORTEngine is an InferenceEngine backed by ONNX Runtime (CPU by default; CUDA optional).
type ORTEngine struct {
	modelPath string
	session   *ort.DynamicAdvancedSession
	inNames   []string
	outNames  []string
	inTypes   map[string]ort.TensorElementDataType
	outTypes  map[string]ort.TensorElementDataType
	mu        sync.Mutex
}

// NewORTEngine loads an ONNX graph with ONNX Runtime.
func NewORTEngine(opts ORTOptions) (*ORTEngine, error) {
	modelPath := strings.TrimSpace(opts.ModelPath)
	if modelPath == "" {
		return nil, fmt.Errorf("ort model path is empty")
	}
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("ort model: %w", err)
	}
	if err := ensureORTEnvironment(opts.SharedLibrary); err != nil {
		return nil, err
	}

	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, fmt.Errorf("GetInputOutputInfo: %w", err)
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		return nil, fmt.Errorf("ort model has no inputs/outputs")
	}
	inNames := make([]string, len(inputs))
	inTypes := make(map[string]ort.TensorElementDataType, len(inputs))
	for i, in := range inputs {
		inNames[i] = in.Name
		inTypes[in.Name] = in.DataType
	}
	outNames := make([]string, len(outputs))
	outTypes := make(map[string]ort.TensorElementDataType, len(outputs))
	for i, out := range outputs {
		outNames[i] = out.Name
		outTypes[out.Name] = out.DataType
	}

	sessionOpts, err := buildORTSessionOptions(opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if sessionOpts != nil {
			_ = sessionOpts.Destroy()
		}
	}()

	session, err := ort.NewDynamicAdvancedSession(modelPath, inNames, outNames, sessionOpts)
	if err != nil {
		return nil, fmt.Errorf("NewDynamicAdvancedSession: %w", err)
	}
	return &ORTEngine{
		modelPath: modelPath,
		session:   session,
		inNames:   inNames,
		outNames:  outNames,
		inTypes:   inTypes,
		outTypes:  outTypes,
	}, nil
}

func buildORTSessionOptions(opts ORTOptions) (*ort.SessionOptions, error) {
	if !opts.GPU {
		return nil, nil
	}
	sessionOpts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("NewSessionOptions: %w", err)
	}
	cudaOpts, err := ort.NewCUDAProviderOptions()
	if err != nil {
		_ = sessionOpts.Destroy()
		return nil, fmt.Errorf("NewCUDAProviderOptions: %w", err)
	}
	defer cudaOpts.Destroy()
	deviceID := opts.CudaDeviceID
	if deviceID < 0 {
		deviceID = 0
	}
	if err := cudaOpts.Update(map[string]string{
		"device_id": fmt.Sprintf("%d", deviceID),
	}); err != nil {
		_ = sessionOpts.Destroy()
		return nil, fmt.Errorf("CUDAProviderOptions.Update: %w", err)
	}
	if err := sessionOpts.AppendExecutionProviderCUDA(cudaOpts); err != nil {
		_ = sessionOpts.Destroy()
		return nil, fmt.Errorf("AppendExecutionProviderCUDA: %w (need a CUDA ORT build under ~/.solcode/lib or jev.ort_lib)", err)
	}
	return sessionOpts, nil
}

func (e *ORTEngine) Name() string { return EngineORT }
func (e *ORTEngine) Ready() bool  { return e != nil && e.session != nil }

func (e *ORTEngine) Close() error {
	if e == nil || e.session == nil {
		return nil
	}
	err := e.session.Destroy()
	e.session = nil
	return err
}

// RunNamed executes one forward pass with family-agnostic named tensors.
func (e *ORTEngine) RunNamed(ctx context.Context, inputs NamedTensors) (NamedOutputs, error) {
	if e == nil || e.session == nil {
		return NamedOutputs{}, ErrEngineNotReady
	}
	if err := ctx.Err(); err != nil {
		return NamedOutputs{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	inVals := make([]ort.Value, len(e.inNames))
	defer destroyValues(inVals)
	for i, name := range e.inNames {
		val, err := e.makeInputValue(name, inputs)
		if err != nil {
			return NamedOutputs{}, err
		}
		inVals[i] = val
	}

	outVals := make([]ort.Value, len(e.outNames))
	if err := e.session.Run(inVals, outVals); err != nil {
		return NamedOutputs{}, fmt.Errorf("ort Run: %w", err)
	}
	defer destroyValues(outVals)

	outs := NamedOutputs{}.ensure()
	for i, name := range e.outNames {
		if outVals[i] == nil {
			return NamedOutputs{}, fmt.Errorf("ort output %q is nil", name)
		}
		switch e.outTypes[name] {
		case ort.TensorElementDataTypeFloat:
			tensor, ok := outVals[i].(*ort.Tensor[float32])
			if !ok {
				return NamedOutputs{}, fmt.Errorf("ort output %q type %T, want float32", name, outVals[i])
			}
			data := tensor.GetData()
			cp := make([]float32, len(data))
			copy(cp, data)
			shape := shapeFromORT(tensor.GetShape())
			outs.Float32[name] = Float32Tensor{Shape: shape, Data: cp}
		default:
			return NamedOutputs{}, fmt.Errorf("unsupported ort output dtype for %q: %v", name, e.outTypes[name])
		}
	}
	return outs, nil
}

func (e *ORTEngine) makeInputValue(name string, inputs NamedTensors) (ort.Value, error) {
	dtype := e.inTypes[name]
	switch dtype {
	case ort.TensorElementDataTypeInt64:
		t, ok := inputs.Int64[name]
		if !ok {
			return nil, fmt.Errorf("missing int64 input %q", name)
		}
		if err := validateFlat(name, t.Shape, len(t.Data)); err != nil {
			return nil, err
		}
		return ort.NewTensor(ort.NewShape(t.Shape...), append([]int64(nil), t.Data...))
	case ort.TensorElementDataTypeBool:
		t, ok := inputs.Bool[name]
		if !ok {
			return nil, fmt.Errorf("missing bool input %q", name)
		}
		if err := validateFlat(name, t.Shape, len(t.Data)); err != nil {
			return nil, err
		}
		return ort.NewTensor(ort.NewShape(t.Shape...), append([]bool(nil), t.Data...))
	default:
		return nil, fmt.Errorf("unsupported ort input dtype for %q: %v", name, dtype)
	}
}

func validateFlat(name string, shape []int64, n int) error {
	want := 1
	for _, d := range shape {
		if d < 0 {
			return fmt.Errorf("input %q has dynamic dim in concrete shape %v", name, shape)
		}
		want *= int(d)
	}
	if want != n {
		return fmt.Errorf("input %q shape %v wants %d elems, got %d", name, shape, want, n)
	}
	return nil
}

func shapeFromORT(s ort.Shape) []int64 {
	out := make([]int64, len(s))
	copy(out, []int64(s))
	return out
}

// Run is an OpenJev convenience wrapper around RunNamed.
func (e *ORTEngine) Run(ctx context.Context, inputs TensorBundle) ([]float32, error) {
	outs, err := e.RunNamed(ctx, namedFromOpenJev(inputs))
	if err != nil {
		return nil, err
	}
	logits := openJevLogits(outs)
	if logits == nil {
		return nil, fmt.Errorf("ort returned no logits")
	}
	return logits, nil
}

func destroyValues(vals []ort.Value) {
	for i, v := range vals {
		if v != nil {
			_ = v.Destroy()
			vals[i] = nil
		}
	}
}

// ResolveORTLibrary picks an explicit path or the default under ~/.solcode/lib.
func ResolveORTLibrary(explicit string) string {
	if p := strings.TrimSpace(explicit); p != "" {
		return p
	}
	return DefaultORTLibrary()
}

// ORTLibraryAvailable reports whether the shared library file exists.
func ORTLibraryAvailable(explicit string) bool {
	p := ResolveORTLibrary(explicit)
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
