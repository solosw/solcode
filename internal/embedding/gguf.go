package embedding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/organizer/yzma"
)

const (
	defaultGGUFContextSize = 2048
	defaultGGUFBatchSize   = 512
	defaultQueryPrefix     = "task: search result | query: "
	defaultDocPrefix       = "title: none | text: "
	defaultGGUFIdleUnload  = 30 * time.Second
)

// ggufProvider embeds text with llama.cpp / EmbeddingGemma GGUF.
//
// It shares the process-global yzma runtime with the memory organizer and loads
// the GGUF on first Embed. The model remains loaded until its idle timer expires
// so CUDA backend work can finish before native resources are released.
type ggufProvider struct {
	modelPath  string
	libDir     string
	dimensions int
	threads    int
	gpuLayers  int
	contextSz  int
	queryPref  string
	docPref    string
	idleAfter  time.Duration

	mu          sync.Mutex
	model       llama.Model
	ctx         llama.Context
	vocab       llama.Vocab
	nEmbd       int32
	ready       bool
	loadErr     error
	closed      bool
	loading     bool
	loadDone    chan struct{}
	lastUsed    time.Time
	unloadTimer *time.Timer
}

func newGGUFProvider(opts Options) (*ggufProvider, error) {
	cfg := opts.Config
	modelPath := strings.TrimSpace(cfg.ModelPath)
	if modelPath == "" {
		modelPath = strings.TrimSpace(opts.ModelDir)
	}
	if modelPath == "" {
		modelPath = resolveDefaultGGUFPath(cfg.Model)
	}
	if modelPath == "" {
		return nil, fmt.Errorf("embedding gguf: model_path is required")
	}
	if !yzma.ModelPresent(modelPath) {
		return nil, fmt.Errorf("embedding gguf: model not found at %s", modelPath)
	}
	libDir := strings.TrimSpace(cfg.LibDir)
	if libDir == "" {
		libDir = yzma.ResolveLibraryDir("")
	}
	ctxSz := cfg.ContextSize
	if ctxSz <= 0 {
		ctxSz = defaultGGUFContextSize
	}
	if ctxSz > 8192 {
		ctxSz = 8192
	}
	idle := time.Duration(cfg.IdleUnloadSec) * time.Second
	if cfg.IdleUnloadSec == 0 {
		idle = defaultGGUFIdleUnload
	}
	if cfg.IdleUnloadSec < 0 {
		idle = -1
	}
	return &ggufProvider{
		modelPath:  modelPath,
		libDir:     libDir,
		dimensions: cfg.Dimensions,
		threads:    cfg.Threads,
		gpuLayers:  cfg.GPULayers,
		contextSz:  ctxSz,
		queryPref:  defaultQueryPrefix,
		docPref:    defaultDocPrefix,
		idleAfter:  idle,
	}, nil
}

func resolveDefaultGGUFPath(modelID string) string {
	shared := config.SharedEmbeddingModelDir()
	candidates := []string{
		filepath.Join(shared, "embeddinggemma-300m_Q4_k_m.gguf"),
		filepath.Join(shared, modelID+".gguf"),
		filepath.Join(shared, modelID+"_Q4_k_m.gguf"),
	}
	if modelID == "" {
		modelID = "embeddinggemma-300m"
		candidates = append([]string{
			filepath.Join(shared, "embeddinggemma-300m_Q4_k_m.gguf"),
		}, candidates...)
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func (p *ggufProvider) ensureLoaded() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if p.ready {
		p.mu.Unlock()
		return
	}
	if p.loading {
		done := p.loadDone
		p.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	p.loading = true
	p.loadErr = nil
	p.loadDone = make(chan struct{})
	done := p.loadDone
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.loading = false
		close(done)
		p.mu.Unlock()
	}()

	// Lock order: runtimeMu first, then p.mu. Organizer and embedding share one
	// CUDA backend; concurrent load/decode/free has crashed Windows builds.
	yzma.LockRuntime()
	defer yzma.UnlockRuntime()

	if err := yzma.EnsureRuntimeLocked(p.libDir); err != nil {
		p.setLoadErr(err)
		return
	}
	useGPU := p.gpuLayers != 0
	if useGPU {
		// Organizer often still holds its GGUF after a successful write; loading
		// embedding on CUDA beside it is the 0xC0000005 path. Evict first.
		yzma.ReleaseOtherGPUHolders(p)
	}
	yzma.LogNative("embed_load_begin", map[string]any{
		"model_path": p.modelPath,
		"gpu":        useGPU,
		"gpu_layers": p.gpuLayers,
		"ctx":        p.contextSz,
	})
	modelParams := llama.ModelDefaultParams()
	modelParams.LoadMode = llama.LoadModeMmap
	modelParams.LazyMode = llama.LazyModeOn
	if !useGPU {
		modelParams.SetCPUOnly()
	} else {
		modelParams.NGpuLayers = int32(p.gpuLayers)
	}
	model, err := llama.ModelLoadFromFile(p.modelPath, modelParams)
	if err != nil {
		yzma.LogNativeErr("embed_load_model", err, map[string]any{"model_path": p.modelPath})
		p.setLoadErr(fmt.Errorf("embedding gguf: load %s: %w", p.modelPath, err))
		return
	}
	ctxParams := llama.ContextDefaultParams()
	ctxParams.NCtx = uint32(p.contextSz)
	batch := uint32(defaultGGUFBatchSize)
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
	if p.threads > 0 {
		ctxParams.NThreads = int32(p.threads)
		ctxParams.NThreadsBatch = int32(p.threads)
	}
	modelCtx, err := llama.InitFromModel(model, ctxParams)
	if err != nil {
		yzma.LogNativeErr("embed_init_ctx", err, map[string]any{"model_path": p.modelPath})
		_ = llama.ModelFree(model)
		p.setLoadErr(fmt.Errorf("embedding gguf: create context: %w", err))
		return
	}
	llama.SetEmbeddings(modelCtx, true)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = llama.Free(modelCtx)
		_ = llama.ModelFree(model)
		return
	}
	p.model = model
	p.ctx = modelCtx
	p.vocab = llama.ModelGetVocab(model)
	p.nEmbd = llama.ModelNEmbd(model)
	p.ready = true
	p.lastUsed = time.Now()
	if useGPU {
		yzma.RegisterGPUHolder(p, p.unloadForPeer)
	}
	p.armIdleUnloadLocked()
	yzma.LogNative("embed_load_ok", map[string]any{"model_path": p.modelPath, "gpu": useGPU, "n_embd": p.nEmbd})
}

func (p *ggufProvider) setLoadErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadErr = err
}

func (p *ggufProvider) armIdleUnloadLocked() {
	if p.idleAfter <= 0 {
		if p.unloadTimer != nil {
			p.unloadTimer.Stop()
			p.unloadTimer = nil
		}
		return
	}
	if p.unloadTimer != nil {
		p.unloadTimer.Stop()
	}
	p.unloadTimer = time.AfterFunc(p.idleAfter, func() {
		p.unloadIfIdle()
	})
}

// freeModelLocked drops the llama context + GGUF.
// Caller must hold runtimeMu then p.mu.
func (p *ggufProvider) freeModelLocked() {
	if p.unloadTimer != nil {
		p.unloadTimer.Stop()
		p.unloadTimer = nil
	}
	modelCtx := p.ctx
	model := p.model
	p.ctx = 0
	p.model = 0
	p.vocab = 0
	p.nEmbd = 0
	p.ready = false
	p.loadErr = nil
	yzma.UnregisterGPUHolder(p)
	yzma.LogNative("embed_free_begin", map[string]any{"had_ctx": modelCtx != 0, "had_model": model != 0})
	if modelCtx != 0 {
		_ = llama.Free(modelCtx)
	}
	if model != 0 {
		_ = llama.ModelFree(model)
	}
	yzma.LogNative("embed_free_ok", nil)
}

// unloadForPeer frees this embedding model when another GPU GGUF is loading.
// runtimeMu is already held; only take p.mu.
func (p *ggufProvider) unloadForPeer() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready || p.closed {
		yzma.UnregisterGPUHolder(p)
		return
	}
	p.freeModelLocked()
}

func (p *ggufProvider) unloadIfIdle() {
	yzma.LockRuntime()
	defer yzma.UnlockRuntime()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.ready || p.loading || p.idleAfter <= 0 {
		return
	}
	if time.Since(p.lastUsed) < p.idleAfter {
		p.armIdleUnloadLocked()
		return
	}
	p.freeModelLocked()
}

func (p *ggufProvider) releaseAfterEmbed() {
	if p == nil || p.idleAfter <= 0 {
		return
	}
	p.armIdleUnloadLocked()
}

func (p *ggufProvider) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.ensureLoaded()
		p.mu.Lock()
		closed := p.closed
		ready := p.ready
		err := p.loadErr
		p.mu.Unlock()
		if closed {
			return fmt.Errorf("embedding gguf: closed")
		}
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("embedding gguf: model not ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (p *ggufProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	return p.embedPrefixed(ctx, text, p.queryPref)
}

func (p *ggufProvider) EmbedDocument(ctx context.Context, text string) ([]float32, error) {
	return p.embedPrefixed(ctx, text, p.docPref)
}

func (p *ggufProvider) embedPrefixed(ctx context.Context, text, prefix string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Windows GPU: run embed in an isolated child so 0xC0000005 cannot kill the agent.
	if yzma.ShouldIsolateNative(p.gpuLayers) {
		return p.embedIsolated(ctx, text, prefix)
	}
	return p.embedInProcess(ctx, prefix+text)
}

func (p *ggufProvider) embedIsolated(ctx context.Context, text, prefix string) ([]float32, error) {
	yzma.LogNative("embed_isolated_begin", map[string]any{
		"model_path": p.modelPath,
		"gpu_layers": p.gpuLayers,
		"ctx":        p.contextSz,
	})
	resp, err := yzma.CallNativeWorker(ctx, yzma.WorkerRequest{
		Op:          "embed",
		ModelPath:   p.modelPath,
		LibDir:      p.libDir,
		ContextSize: p.contextSz,
		Threads:     p.threads,
		GPULayers:   p.gpuLayers,
		Dimensions:  p.dimensions,
		Text:        text,
		Prefix:      prefix,
	})
	if err != nil {
		yzma.LogNativeErr("embed_isolated_fail", err, map[string]any{"exit": resp.Exit})
		return nil, fmt.Errorf("embedding gguf isolated: %w", err)
	}
	if len(resp.Vector) == 0 {
		return nil, fmt.Errorf("embedding gguf isolated: empty vector")
	}
	yzma.LogNative("embed_isolated_ok", map[string]any{"dims": len(resp.Vector)})
	// Worker already L2-normalized / truncated; return a copy.
	out := make([]float32, len(resp.Vector))
	copy(out, resp.Vector)
	return out, nil
}

func (p *ggufProvider) embedInProcess(ctx context.Context, prefixed string) ([]float32, error) {
	yzma.LogNative("embed_begin", map[string]any{
		"model_path": p.modelPath,
		"gpu_layers": p.gpuLayers,
		"isolated":   false,
	})
	if err := p.waitReady(ctx); err != nil {
		yzma.LogNativeErr("embed_wait_ready", err, nil)
		return nil, err
	}
	// Lock order: runtimeMu first, then p.mu.
	yzma.LockRuntime()
	defer yzma.UnlockRuntime()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.ready {
		return nil, fmt.Errorf("embedding gguf: not ready")
	}
	p.lastUsed = time.Now()
	if p.idleAfter > 0 {
		defer p.releaseAfterEmbed()
	} else {
		p.armIdleUnloadLocked()
	}

	if mem, err := llama.GetMemory(p.ctx); err == nil && mem != 0 {
		_ = llama.MemoryClear(mem, true)
	}
	tokens := llama.Tokenize(p.vocab, prefixed, true, true)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("embedding gguf: empty tokenization")
	}
	if len(tokens) > p.contextSz {
		tokens = tokens[:p.contextSz]
	}
	batch := llama.BatchGetOne(tokens)
	if _, err := llama.Decode(p.ctx, batch); err != nil {
		yzma.LogNativeErr("embed_decode", err, nil)
		return nil, fmt.Errorf("embedding gguf: decode: %w", err)
	}
	vec, err := llama.GetEmbeddingsSeq(p.ctx, 0, p.nEmbd)
	if err != nil {
		yzma.LogNativeErr("embed_get", err, nil)
		return nil, fmt.Errorf("embedding gguf: get embeddings: %w", err)
	}
	if len(vec) == 0 {
		// Some builds return a flat buffer through GetEmbeddings.
		flat, flatErr := llama.GetEmbeddings(p.ctx, 1, int(p.nEmbd))
		if flatErr != nil {
			return nil, fmt.Errorf("embedding gguf: empty embeddings")
		}
		vec = flat
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("embedding gguf: empty embeddings")
	}
	// GetEmbeddingsSeq returns a view into llama memory; copy before free.
	out := make([]float32, len(vec))
	copy(out, vec)
	normed := truncateAndNormalize(out, p.dimensions)
	yzma.LogNative("embed_ok", map[string]any{"dims": len(normed)})
	return normed, nil
}

func (p *ggufProvider) Close() error {
	yzma.LockRuntime()
	defer yzma.UnlockRuntime()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.freeModelLocked()
	return nil
}

var (
	_ Provider         = (*ggufProvider)(nil)
	_ DocumentEmbedder = (*ggufProvider)(nil)
)
