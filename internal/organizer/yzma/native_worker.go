package yzma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/solosw/solcode/internal/organizer"
)

// Native worker protocol: parent spawns `solcode -native-worker`, writes one
// JSON request on stdin, reads one JSON response on stdout. The child loads
// llama.cpp, runs a single generate or embed, then exits. A Windows access
// violation (0xC0000005) therefore kills only the worker — the agent stays up
// and the failure is logged.

const (
	workerEnvMarker      = "SOLCODE_NATIVE_WORKER"
	isolateEnvDisable    = "SOLCODE_ISOLATE_NATIVE" // "0" disables subprocess isolation
	defaultWorkerTimeout = 10 * time.Minute
)

// WorkerRequest is one isolated native op.
type WorkerRequest struct {
	Op          string `json:"op"` // "generate" | "embed"
	ModelPath   string `json:"model_path"`
	LibDir      string `json:"lib_dir"`
	ContextSize int    `json:"context_size"`
	Threads     int    `json:"threads"`
	GPULayers   int    `json:"gpu_layers"`
	Dimensions  int    `json:"dimensions,omitempty"`
	// generate
	System      string  `json:"system,omitempty"`
	User        string  `json:"user,omitempty"`
	Grammar     string  `json:"grammar,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	// embed
	Text   string `json:"text,omitempty"`
	Prefix string `json:"prefix,omitempty"` // full prefix already applied by parent, or empty
}

// WorkerResponse is the worker's single reply.
type WorkerResponse struct {
	OK     bool      `json:"ok"`
	Text   string    `json:"text,omitempty"`
	Vector []float32 `json:"vector,omitempty"`
	Error  string    `json:"error,omitempty"`
	Exit   int       `json:"exit,omitempty"`
}

// RunningAsWorker reports whether this process is the isolated native child.
func RunningAsWorker() bool {
	return strings.TrimSpace(os.Getenv(workerEnvMarker)) == "1"
}

// ShouldIsolateNative reports whether GPU llama work should run out-of-process
// so a CUDA access violation cannot tear down the agent.
func ShouldIsolateNative(gpuLayers int) bool {
	if RunningAsWorker() {
		return false
	}
	if v := strings.TrimSpace(os.Getenv(isolateEnvDisable)); v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	// Isolation is primarily for Windows CUDA; CPU paths stay in-process.
	if runtime.GOOS != "windows" {
		return false
	}
	return gpuLayers != 0
}

// RunNativeWorker is the child entrypoint: one JSON request on in, one JSON
// response on out, then return. Callers should os.Exit after this.
func RunNativeWorker(in io.Reader, out io.Writer) int {
	LogNative("worker_start", map[string]any{"role": "child"})
	var req WorkerRequest
	dec := json.NewDecoder(in)
	if err := dec.Decode(&req); err != nil {
		writeWorkerResp(out, WorkerResponse{OK: false, Error: fmt.Sprintf("decode request: %v", err)})
		return 2
	}
	LogNative("worker_request", map[string]any{
		"op":         req.Op,
		"model_path": req.ModelPath,
		"gpu_layers": req.GPULayers,
		"ctx":        req.ContextSize,
	})

	ctx, cancel := context.WithTimeout(context.Background(), defaultWorkerTimeout)
	defer cancel()

	var resp WorkerResponse
	switch strings.ToLower(strings.TrimSpace(req.Op)) {
	case "generate":
		text, err := workerGenerate(ctx, req)
		if err != nil {
			resp = WorkerResponse{OK: false, Error: err.Error()}
			LogNativeErr("worker_generate", err, nil)
		} else {
			resp = WorkerResponse{OK: true, Text: text}
			LogNative("worker_generate_ok", map[string]any{"chars": len(text)})
		}
	case "embed":
		vec, err := workerEmbed(ctx, req)
		if err != nil {
			resp = WorkerResponse{OK: false, Error: err.Error()}
			LogNativeErr("worker_embed", err, nil)
		} else {
			resp = WorkerResponse{OK: true, Vector: vec}
			LogNative("worker_embed_ok", map[string]any{"dims": len(vec)})
		}
	default:
		resp = WorkerResponse{OK: false, Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
	writeWorkerResp(out, resp)
	if resp.OK {
		return 0
	}
	return 1
}

func writeWorkerResp(out io.Writer, resp WorkerResponse) {
	enc := json.NewEncoder(out)
	_ = enc.Encode(resp)
	if f, ok := out.(*os.File); ok {
		_ = f.Sync()
	}
}

// workerChildEnv builds the child process environment so ggml/CUDA DLLs beside
// llama.dll resolve even when the parent did not already have that dir on PATH.
func workerChildEnv(libDir string) []string {
	libDir = ResolveLibraryDir(libDir)
	env := append([]string(nil), os.Environ()...)
	env = append(env, workerEnvMarker+"=1")
	if libDir == "" {
		return env
	}
	env = append(env, "YZMA_LIB="+libDir)
	pathUpdated := false
	for i, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if !strings.EqualFold(kv[:eq], "Path") {
			continue
		}
		env[i] = kv[:eq] + "=" + libDir + string(os.PathListSeparator) + kv[eq+1:]
		pathUpdated = true
		break
	}
	if !pathUpdated {
		env = append(env, "Path="+libDir)
	}
	return env
}

func workerGenerate(ctx context.Context, req WorkerRequest) (string, error) {
	gen := New(Config{
		ModelPath:       req.ModelPath,
		LibDir:          req.LibDir,
		ContextSize:     req.ContextSize,
		Threads:         req.Threads,
		GPULayers:       req.GPULayers,
		IdleUnloadAfter: -1, // stay loaded for this one shot; process exits after
	})
	defer func() { _ = gen.Close() }()
	return gen.Generate(ctx, organizer.GenerateRequest{
		System:      req.System,
		User:        req.User,
		Grammar:     req.Grammar,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	})
}

func workerEmbed(ctx context.Context, req WorkerRequest) ([]float32, error) {
	_ = ctx
	LogNative("worker_embed_load", map[string]any{"model": req.ModelPath, "gpu_layers": req.GPULayers})
	if err := EnsureRuntime(req.LibDir); err != nil {
		return nil, err
	}
	ctxSz := req.ContextSize
	if ctxSz <= 0 {
		ctxSz = 2048
	}
	LockRuntime()
	defer UnlockRuntime()

	// Reuse the same GPU→CPU fallback path as in-process embedding.
	model, modelCtx, usedGPU, err := loadEmbeddingModelForWorker(req.ModelPath, req.GPULayers, ctxSz, req.Threads)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = llama.Free(modelCtx)
		_ = llama.ModelFree(model)
	}()
	LogNative("worker_embed_loaded", map[string]any{"gpu": usedGPU, "n_embd": llama.ModelNEmbd(model)})
	llama.SetEmbeddings(modelCtx, true)
	vocab := llama.ModelGetVocab(model)
	nEmbd := llama.ModelNEmbd(model)

	prefixed := req.Text
	if p := strings.TrimSpace(req.Prefix); p != "" {
		prefixed = p + req.Text
	}
	if mem, err := llama.GetMemory(modelCtx); err == nil && mem != 0 {
		_ = llama.MemoryClear(mem, true)
	}
	tokens := llama.Tokenize(vocab, prefixed, true, true)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("worker embed: empty tokenization")
	}
	if len(tokens) > ctxSz {
		tokens = tokens[:ctxSz]
	}
	batchTok := llama.BatchGetOne(tokens)
	if _, err := llama.Decode(modelCtx, batchTok); err != nil {
		return nil, fmt.Errorf("worker embed decode: %w", err)
	}
	vec, err := llama.GetEmbeddingsSeq(modelCtx, 0, nEmbd)
	if err != nil || len(vec) == 0 {
		flat, flatErr := llama.GetEmbeddings(modelCtx, 1, int(nEmbd))
		if flatErr != nil || len(flat) == 0 {
			if err != nil {
				return nil, fmt.Errorf("worker embed get: %w", err)
			}
			return nil, fmt.Errorf("worker embed: empty embeddings")
		}
		vec = flat
	}
	out := make([]float32, len(vec))
	copy(out, vec)
	if req.Dimensions > 0 && req.Dimensions < len(out) {
		out = out[:req.Dimensions]
	}
	var sum float64
	for _, x := range out {
		sum += float64(x) * float64(x)
	}
	if sum > 0 {
		inv := float32(1 / math.Sqrt(sum))
		for i := range out {
			out[i] *= inv
		}
	}
	return out, nil
}

// loadEmbeddingModelForWorker mirrors embedding.gguf GPU→CPU fallback without
// importing the embedding package (would create a cycle through yzma).
func loadEmbeddingModelForWorker(modelPath string, gpuLayers, contextSz, threads int) (llama.Model, llama.Context, bool, error) {
	tryGPU := gpuLayers != 0
	model, modelCtx, err := initWorkerEmbedHandles(modelPath, tryGPU, gpuLayers, contextSz, threads)
	if err == nil {
		return model, modelCtx, tryGPU, nil
	}
	if !tryGPU {
		return 0, 0, false, err
	}
	LogNativeErr("worker_embed_gpu_fail_fallback_cpu", err, map[string]any{"model_path": modelPath})
	model, modelCtx, err = initWorkerEmbedHandles(modelPath, false, 0, contextSz, threads)
	if err != nil {
		return 0, 0, false, err
	}
	LogNative("worker_embed_cpu_fallback_ok", map[string]any{"model_path": modelPath})
	return model, modelCtx, false, nil
}

func initWorkerEmbedHandles(modelPath string, useGPU bool, gpuLayers, contextSz, threads int) (llama.Model, llama.Context, error) {
	modelParams := llama.ModelDefaultParams()
	modelParams.LoadMode = llama.LoadModeMmap
	modelParams.LazyMode = llama.LazyModeOn
	if !useGPU {
		modelParams.SetCPUOnly()
	} else {
		modelParams.NGpuLayers = int32(gpuLayers)
	}
	model, err := llama.ModelLoadFromFile(modelPath, modelParams)
	if err != nil {
		return 0, 0, fmt.Errorf("worker embed load: %w", err)
	}
	ctxParams := llama.ContextDefaultParams()
	ctxParams.NCtx = uint32(contextSz)
	batch := uint32(512)
	if batch > ctxParams.NCtx {
		batch = ctxParams.NCtx
	}
	ctxParams.NBatch = batch
	ctxParams.NUbatch = batch
	ctxParams.Embeddings = 1
	ctxParams.PoolingType = llama.PoolingTypeMean
	ctxParams.TypeK = llama.GGMLTypeQ8_0
	ctxParams.TypeV = llama.GGMLTypeQ8_0
	if useGPU {
		ctxParams.FlashAttentionType = llama.FlashAttentionTypeDisabled
	}
	if threads > 0 {
		ctxParams.NThreads = int32(threads)
		ctxParams.NThreadsBatch = int32(threads)
	}
	modelCtx, err := llama.InitFromModel(model, ctxParams)
	if err != nil {
		_ = llama.ModelFree(model)
		return 0, 0, fmt.Errorf("worker embed context: %w", err)
	}
	return model, modelCtx, nil
}

// CallNativeWorker spawns this executable as a -native-worker child and runs req.
func CallNativeWorker(ctx context.Context, req WorkerRequest) (WorkerResponse, error) {
	exe, err := os.Executable()
	if err != nil {
		return WorkerResponse{}, fmt.Errorf("native worker: executable: %w", err)
	}
	LogNative("worker_spawn", map[string]any{
		"op":         req.Op,
		"model_path": req.ModelPath,
		"gpu_layers": req.GPULayers,
		"exe":        exe,
	})

	body, err := json.Marshal(req)
	if err != nil {
		return WorkerResponse{}, err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	cmdCtx := ctx
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		cmdCtx, cancel = context.WithTimeout(ctx, defaultWorkerTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(cmdCtx, exe, "-native-worker")
	cmd.Env = workerChildEnv(req.LibDir)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	raw := bytes.TrimSpace(stdout.Bytes())
	var resp WorkerResponse
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			LogNative("worker_bad_json", map[string]any{
				"exit":   exitCode,
				"stdout": truncateDiag(string(raw), 500),
				"stderr": truncateDiag(stderr.String(), 500),
				"error":  err.Error(),
			})
			resp.Error = fmt.Sprintf("worker json: %v; stderr=%s", err, truncateDiag(stderr.String(), 300))
		}
	}

	if runErr != nil {
		// Prefer the child's JSON error (e.g. "failed to initialize model") over
		// a bare "exit status 1" so compact.log / callers see the real cause.
		detail := strings.TrimSpace(resp.Error)
		if detail == "" {
			detail = fmt.Sprintf("native worker exited: %v (exit=%d)", runErr, exitCode)
			if exitCode == -1073741819 || exitCode == 3221225473 { // 0xC0000005
				detail = fmt.Sprintf("native worker access violation 0xC0000005 (exit=%d); agent continues", exitCode)
			}
			if stderr.Len() > 0 {
				detail += "; stderr=" + truncateDiag(stderr.String(), 400)
			}
		} else if exitCode == -1073741819 || exitCode == 3221225473 {
			detail = fmt.Sprintf("native worker access violation 0xC0000005 (exit=%d); %s", exitCode, detail)
		}
		resp.OK = false
		resp.Exit = exitCode
		if resp.Error == "" {
			resp.Error = detail
		}
		LogNativeErr("worker_crash_or_fail", fmt.Errorf("%s", detail), map[string]any{
			"exit":   exitCode,
			"op":     req.Op,
			"stderr": truncateDiag(stderr.String(), 400),
		})
		return resp, fmt.Errorf("%s", detail)
	}

	if !resp.OK {
		errMsg := strings.TrimSpace(resp.Error)
		if errMsg == "" {
			errMsg = "native worker returned ok=false"
		}
		LogNativeErr("worker_op_failed", fmt.Errorf("%s", errMsg), map[string]any{"op": req.Op})
		return resp, fmt.Errorf("%s", errMsg)
	}
	LogNative("worker_done", map[string]any{"op": req.Op, "exit": exitCode})
	return resp, nil
}

func truncateDiag(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
