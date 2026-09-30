package yzma

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/solosw/solcode/internal/organizer"
)

// Config configures the in-process llama.cpp generator.
type Config struct {
	// ModelPath is the GGUF file to load.
	ModelPath string
	// LibDir is the llama.cpp shared-library directory.
	LibDir string
	// ContextSize is the context window in tokens.
	ContextSize int
	// Threads bounds inference threads. Zero lets llama.cpp decide.
	Threads int
	// GPULayers offloads that many layers to the GPU. Negative means all.
	GPULayers int
	// IdleUnloadAfter frees the model after this idle duration.
	// Zero uses idleUnloadAfter default; negative disables unload.
	IdleUnloadAfter time.Duration
}

const (
	// defaultContextSize is the KV window when config leaves ContextSize unset.
	// 16k holds system + side-context + a long compact transcript + generation
	// headroom. Physical RAM is kept down by mmap weights, Q8 KV, small n_batch,
	// and idle unload after silence.
	defaultContextSize = 16384
	// maxContextSize caps misconfigured huge windows so a typo cannot OOM the host.
	maxContextSize = 16384
	// defaultBatchSize is the decode batch. It must stay << context size: llama.cpp
	// allocates scratch proportional to n_batch, and setting n_batch == n_ctx is
	// the main reason a 1B Q4 model still sits at multi-gigabyte RSS.
	defaultBatchSize = 256
	// maxBatchSize hard-caps n_batch regardless of context size.
	maxBatchSize = 256
	// idleUnloadAfter frees the GGUF after silence. Unload is delayed (not immediate)
	// so the Windows CUDA backend can finish deferred work first; multi-workspace
	// processes still must not pin models forever.
	idleUnloadAfter = 30 * time.Second
	// pieceBufferSize is the scratch buffer for one detokenized piece.
	pieceBufferSize = 512
)

// generator implements organizer.LocalGenerator on top of llama.cpp.
//
// The GGUF is loaded on first Generate (or an explicit waitForReady), not at
// construction: mapping a multi-hundred-MB model at process start was the
// dominant reason solcode jumped from tens of MB to multi-GB RSS. After idle
// silence the model is unloaded again.
type generator struct {
	cfg Config

	mu          sync.Mutex
	model       llama.Model
	ctx         llama.Context
	vocab       llama.Vocab
	ready       bool
	loadErr     error
	closed      bool
	loading     bool
	lastUsed    time.Time
	unloadTimer *time.Timer
	loadDone    chan struct{} // closed when a load attempt finishes; recreated per load
}

// New builds a generator without loading the GGUF yet.
//
// Ready stays false until the first Generate (or waitForReady) triggers a load.
// Construction never fails on a missing library so solcode keeps running; the
// failure surfaces when Organize actually needs the model.
func New(cfg Config) organizer.LocalGenerator {
	return &generator{cfg: normalizeConfig(cfg)}
}

func normalizeConfig(cfg Config) Config {
	if cfg.ContextSize <= 0 {
		cfg.ContextSize = defaultContextSize
	}
	if cfg.ContextSize > maxContextSize {
		cfg.ContextSize = maxContextSize
	}
	if cfg.IdleUnloadAfter == 0 {
		cfg.IdleUnloadAfter = idleUnloadAfter
	}
	return cfg
}

func (g *generator) idleAfter() time.Duration {
	if g == nil {
		return idleUnloadAfter
	}
	if g.cfg.IdleUnloadAfter < 0 {
		return 0 // disabled
	}
	if g.cfg.IdleUnloadAfter > 0 {
		return g.cfg.IdleUnloadAfter
	}
	return idleUnloadAfter
}

func (g *generator) contextSize() int {
	if g == nil {
		return defaultContextSize
	}
	if g.cfg.ContextSize > 0 {
		return g.cfg.ContextSize
	}
	return defaultContextSize
}

func (g *generator) batchSize() int {
	ctxSize := g.contextSize()
	batch := defaultBatchSize
	if batch > ctxSize {
		batch = ctxSize
	}
	if batch > maxBatchSize {
		batch = maxBatchSize
	}
	if batch < 64 {
		batch = 64
	}
	return batch
}

// libBindOnce serializes llama.Load across generators in the process. The binding
// is process-global, so the first successful load wins.
var libBindOnce sync.Once

// backendOnce serializes ggml backend registration, which is also process-global.
// backendErr records the outcome of that single registration attempt.
var (
	backendOnce sync.Once
	backendErr  error
)

func (g *generator) Name() string { return "yzma-llama.cpp" }

func (g *generator) Ready() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ready && !g.closed
}

// ensureLoaded loads the GGUF on demand. Concurrent callers share one attempt.
// Unlike the old startup preload, this only runs when Organize/Generate needs
// the model, so idle solcode does not hold multi-GB RSS.
//
// Native load/free run under the process-wide runtime lock so organizer and
// embedding cannot touch CUDA backends concurrently.
func (g *generator) ensureLoaded() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	if g.ready {
		g.mu.Unlock()
		return
	}
	if g.loading {
		done := g.loadDone
		g.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	g.loading = true
	g.loadErr = nil
	g.loadDone = make(chan struct{})
	done := g.loadDone
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.loading = false
		close(done)
		g.mu.Unlock()
	}()

	modelPath := strings.TrimSpace(g.cfg.ModelPath)
	if modelPath == "" {
		g.setLoadErr(fmt.Errorf("yzma: model path is empty"))
		return
	}
	if !ModelPresent(modelPath) {
		g.setLoadErr(fmt.Errorf("yzma: GGUF model not found at %s", modelPath))
		return
	}

	// Lock order: runtimeMu first, then g.mu. Holds the process-wide CUDA lock
	// for the entire native load so embedding cannot free/decode mid-load.
	LockRuntime()
	defer UnlockRuntime()

	libDir := ResolveLibraryDir(g.cfg.LibDir)
	if err := EnsureRuntimeLocked(libDir); err != nil {
		g.setLoadErr(err)
		return
	}

	useGPU := g.cfg.GPULayers != 0
	if useGPU {
		// Windows CUDA cannot safely keep organizer + embedding GGUFs resident
		// together; free the other GPU holder before loading this one.
		ReleaseOtherGPUHolders(g)
	}
	LogNative("organizer_load_begin", map[string]any{
		"model_path": modelPath,
		"gpu":        useGPU,
		"gpu_layers": g.cfg.GPULayers,
		"ctx":        g.contextSize(),
	})

	modelParams := llama.ModelDefaultParams()
	// mmap keeps weights file-backed so the OS can share the same GGUF pages
	// across multiple solcode workspaces instead of each process private-copying.
	// Never mlock: that would pin RSS and defeat multi-workspace.
	modelParams.LoadMode = llama.LoadModeMmap
	modelParams.LazyMode = llama.LazyModeOn
	if !useGPU {
		modelParams.SetCPUOnly()
	} else {
		modelParams.NGpuLayers = int32(g.cfg.GPULayers)
	}
	model, err := llama.ModelLoadFromFile(modelPath, modelParams)
	if err != nil {
		LogNativeErr("organizer_load_model", err, map[string]any{"model_path": modelPath})
		g.setLoadErr(fmt.Errorf("yzma: load GGUF %s: %w", modelPath, err))
		return
	}

	ctxParams := llama.ContextDefaultParams()
	contextSize := g.contextSize()
	batchSize := g.batchSize()
	ctxParams.NCtx = uint32(contextSize)
	// Keep n_batch << n_ctx. Setting them equal was the main multi-GB RSS cause:
	// llama.cpp scratch/KV scales with batch*ctx, not just model weights.
	ctxParams.NBatch = uint32(batchSize)
	if ctxParams.NUbatch == 0 || ctxParams.NUbatch > ctxParams.NBatch {
		ctxParams.NUbatch = ctxParams.NBatch
	}
	// Q8 KV is ~2x smaller than F16 and is the main lever once n_ctx is 16k.
	ctxParams.TypeK = llama.GGMLTypeQ8_0
	ctxParams.TypeV = llama.GGMLTypeQ8_0
	// Disable flash-attn/CUDA graphs on GPU: dual-model and graph-reuse paths
	// have produced 0xC0000005 on Windows after successful writes.
	if useGPU {
		ctxParams.FlashAttentionType = llama.FlashAttentionTypeDisabled
	} else {
		ctxParams.FlashAttentionType = llama.FlashAttentionTypeAuto
	}
	if g.cfg.Threads > 0 {
		ctxParams.NThreads = int32(g.cfg.Threads)
		ctxParams.NThreadsBatch = int32(g.cfg.Threads)
	}
	modelCtx, err := llama.InitFromModel(model, ctxParams)
	if err != nil {
		LogNativeErr("organizer_init_ctx", err, map[string]any{"model_path": modelPath})
		_ = llama.ModelFree(model)
		g.setLoadErr(fmt.Errorf("yzma: create context: %w", err))
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		_ = llama.Free(modelCtx)
		_ = llama.ModelFree(model)
		return
	}
	g.model = model
	g.ctx = modelCtx
	g.vocab = llama.ModelGetVocab(model)
	g.ready = true
	g.lastUsed = time.Now()
	if useGPU {
		RegisterGPUHolder(g, g.unloadForPeer)
	}
	g.armIdleUnloadLocked()
	LogNative("organizer_load_ok", map[string]any{"model_path": modelPath, "gpu": useGPU})
}

func (g *generator) setLoadErr(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadErr = err
}

// armIdleUnloadLocked schedules unload after idle. Caller holds g.mu.
func (g *generator) armIdleUnloadLocked() {
	after := g.idleAfter()
	if after <= 0 {
		if g.unloadTimer != nil {
			g.unloadTimer.Stop()
			g.unloadTimer = nil
		}
		return
	}
	if g.unloadTimer != nil {
		g.unloadTimer.Stop()
	}
	g.unloadTimer = time.AfterFunc(after, func() {
		g.unloadIfIdle()
	})
}

// freeModelLocked drops the llama context + GGUF.
// Caller must hold runtimeMu then g.mu (see LockRuntime / UnlockRuntime).
func (g *generator) freeModelLocked() {
	if g.unloadTimer != nil {
		g.unloadTimer.Stop()
		g.unloadTimer = nil
	}
	modelCtx := g.ctx
	model := g.model
	g.ctx = 0
	g.model = 0
	g.vocab = 0
	g.ready = false
	// Allow a later ensureLoaded to try again after free.
	g.loadErr = nil
	UnregisterGPUHolder(g)
	LogNative("organizer_free_begin", map[string]any{"had_ctx": modelCtx != 0, "had_model": model != 0})
	if modelCtx != 0 {
		_ = llama.Free(modelCtx)
	}
	if model != 0 {
		_ = llama.ModelFree(model)
	}
	LogNative("organizer_free_ok", nil)
}

// unloadForPeer frees this generator when another GPU model is about to load.
// runtimeMu is already held by the peer; only take g.mu here.
func (g *generator) unloadForPeer() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.ready || g.closed {
		UnregisterGPUHolder(g)
		return
	}
	g.freeModelLocked()
}

func (g *generator) unloadIfIdle() {
	if g == nil {
		return
	}
	LockRuntime()
	defer UnlockRuntime()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || !g.ready || g.loading {
		return
	}
	after := g.idleAfter()
	if after <= 0 {
		return
	}
	if time.Since(g.lastUsed) < after {
		g.armIdleUnloadLocked()
		return
	}
	g.freeModelLocked()
}

// releaseAfterGenerate schedules the idle unload instead of freeing native
// CUDA resources synchronously on the inference return path.
func (g *generator) releaseAfterGenerate() {
	if g == nil || g.idleAfter() <= 0 {
		return
	}
	g.armIdleUnloadLocked()
}

// LoadError returns the deferred load failure, if any.
func (g *generator) LoadError() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.loadErr
}

// Generate runs one completion under the process-wide llama.cpp lock.
//
// A single llama.cpp context is not safe for concurrent decoding. Organizer and
// GGUF embedding also share one CUDA backend in-process, so every native call
// serializes on runtimeMu. The GGUF loads on demand and stays resident until the
// idle timer unloads it.
//
// On Windows with GPU layers, Generate is delegated to an isolated -native-worker
// child process so a CUDA access violation (0xC0000005) cannot tear down the
// agent. Failures return as errors and are logged to native_gpu.log.
func (g *generator) Generate(ctx context.Context, req organizer.GenerateRequest) (string, error) {
	if g == nil {
		return "", organizer.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ShouldIsolateNative(g.cfg.GPULayers) {
		return g.generateIsolated(ctx, req)
	}
	return g.generateInProcess(ctx, req)
}

func (g *generator) generateIsolated(ctx context.Context, req organizer.GenerateRequest) (string, error) {
	LogNative("generate_isolated_begin", map[string]any{
		"model_path": g.cfg.ModelPath,
		"gpu_layers": g.cfg.GPULayers,
		"ctx":        g.contextSize(),
	})
	resp, err := CallNativeWorker(ctx, WorkerRequest{
		Op:          "generate",
		ModelPath:   g.cfg.ModelPath,
		LibDir:      ResolveLibraryDir(g.cfg.LibDir),
		ContextSize: g.contextSize(),
		Threads:     g.cfg.Threads,
		GPULayers:   g.cfg.GPULayers,
		System:      req.System,
		User:        req.User,
		Grammar:     req.Grammar,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	})
	if err != nil {
		LogNativeErr("generate_isolated_fail", err, map[string]any{"exit": resp.Exit})
		return "", fmt.Errorf("%w: isolated generate: %v", organizer.ErrUnavailable, err)
	}
	LogNative("generate_isolated_ok", map[string]any{"chars": len(resp.Text)})
	return resp.Text, nil
}

func (g *generator) generateInProcess(ctx context.Context, req organizer.GenerateRequest) (string, error) {
	LogNative("generate_begin", map[string]any{
		"model_path": g.cfg.ModelPath,
		"gpu_layers": g.cfg.GPULayers,
		"isolated":   false,
	})
	// Load outside the decode section so concurrent callers share one attempt.
	g.ensureLoaded()

	// Lock order: runtimeMu first, then g.mu.
	LockRuntime()
	defer UnlockRuntime()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return "", fmt.Errorf("%w: closed", organizer.ErrUnavailable)
	}
	if !g.ready {
		if g.loadErr != nil {
			return "", fmt.Errorf("%w: %v", organizer.ErrUnavailable, g.loadErr)
		}
		return "", fmt.Errorf("%w: still loading", organizer.ErrUnavailable)
	}
	g.lastUsed = time.Now()
	if g.idleAfter() > 0 {
		defer g.releaseAfterGenerate()
	}

	// Each Generate is an independent completion on a shared context, so the
	// KV/recurrent state from any previous call must be wiped first. Without
	// this, BatchGetOne's auto-tracked positions collide with leftover cache
	// and the model either emits EOG immediately or produces garbage.
	if mem, err := llama.GetMemory(g.ctx); err == nil && mem != 0 {
		_ = llama.MemoryClear(mem, true)
	}

	prompt, usedTemplate := g.applyChatTemplate(req.System, req.User)
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("yzma: chat template rendered an empty prompt")
	}
	// parseSpecial must stay true so <|im_start|> / <|im_end|> from the
	// template become real special tokens rather than literal text.
	//
	// addSpecial (BOS) is only for the plain-concatenation fallback: a real
	// chat template (MiniCPM's includes `{{- bos_token }}`, chatml emits the
	// role markers itself) already shaped the prompt, and a second BOS confuses
	// small models into garbage completions. Every Generate clears the KV cache
	// first, so this is always a fresh "first" message either way.
	tokens := llama.Tokenize(g.vocab, prompt, !usedTemplate, true)
	if len(tokens) == 0 {
		return "", fmt.Errorf("yzma: prompt tokenized to nothing")
	}
	// Cap prompt to context - generation headroom so Decode cannot OOM the KV.
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
	}
	ctxSize := g.contextSize()
	// Generation must fit in the window, but never steal more than half the
	// context from the prompt — otherwise a large MaxTokens leaves almost no
	// room for system+user and the tail-trim drops the instructions.
	if maxTokens > ctxSize/2 {
		maxTokens = ctxSize / 2
		if maxTokens < 256 {
			maxTokens = 256
		}
	}
	headroom := maxTokens + 64
	if headroom >= ctxSize {
		headroom = ctxSize / 4
		if headroom < 64 {
			headroom = 64
		}
	}
	maxPrompt := ctxSize - headroom
	if maxPrompt < 64 {
		maxPrompt = 64
	}
	if len(tokens) > maxPrompt {
		// Keep the tail (user payload / transcript end) which usually carries
		// the durable facts; drop the leading system preamble tokens first.
		tokens = tokens[len(tokens)-maxPrompt:]
	}

	smpl := g.buildSampler(req)
	if smpl != 0 {
		defer llama.SamplerFree(smpl)
	}

	var out strings.Builder
	pieceBuf := make([]byte, pieceBufferSize)

	// The first batch carries the whole prompt; every later batch carries the
	// single token just sampled. decoded bounds the generated tokens, so a bogus
	// (empty) batch cannot spin forever.
	//
	// Do NOT call SamplerAccept after SamplerSample: llama_sampler_sample already
	// accepts into the chain, and a second Accept crashes the grammar sampler
	// (see .solcode/step.log). The official yzma chat/hello examples never call it.
	batch := llama.BatchGetOne(tokens)
	if batch.NTokens <= 0 {
		return "", fmt.Errorf("yzma: empty decode batch for a non-empty prompt")
	}
	for decoded := 0; decoded < maxTokens; decoded++ {
		if err := ctx.Err(); err != nil {
			return out.String(), err
		}
		if _, err := llama.Decode(g.ctx, batch); err != nil {
			LogNativeErr("organizer_decode", err, map[string]any{"decoded": decoded})
			return out.String(), fmt.Errorf("yzma: decode: %w", err)
		}
		next := llama.SamplerSample(smpl, g.ctx, -1)
		if next == llama.TokenNull || llama.VocabIsEOG(g.vocab, next) {
			break
		}
		if n := llama.TokenToPiece(g.vocab, next, pieceBuf, 0, false); n > 0 {
			if int(n) > len(pieceBuf) {
				n = int32(len(pieceBuf))
			}
			out.Write(pieceBuf[:n])
		}
		batch = llama.BatchGetOne([]llama.Token{next})
		if batch.NTokens <= 0 {
			break
		}
	}
	LogNative("generate_ok", map[string]any{"chars": out.Len()})
	return out.String(), nil
}

// buildSampler assembles the sampling chain: optional grammar constraint,
// temperature (or greedy), and a distribution sampler.
func (g *generator) buildSampler(req organizer.GenerateRequest) llama.Sampler {
	chainParams := llama.SamplerChainDefaultParams()
	chain := llama.SamplerChainInit(chainParams)
	if chain == 0 {
		return 0
	}
	if grammar := strings.TrimSpace(req.Grammar); grammar != "" {
		if grammarSampler := llama.SamplerInitGrammar(g.vocab, grammar, "root"); grammarSampler != 0 {
			llama.SamplerChainAdd(chain, grammarSampler)
		}
	}
	if req.Temperature <= 0 {
		llama.SamplerChainAdd(chain, llama.SamplerInitGreedy())
	} else {
		llama.SamplerChainAdd(chain, llama.SamplerInitTemp(float32(req.Temperature)))
		llama.SamplerChainAdd(chain, llama.SamplerInitDist(llama.DefaultSeed))
	}
	return chain
}

// applyChatTemplate renders system+user through a chat template.
//
// The returned bool is true when a real chat template shaped the prompt (so the
// caller must NOT ask Tokenize to add BOS again). False means we fell back to a
// plain concatenation and Tokenize should add special tokens itself.
//
// Buffer / return-value rules for llama_chat_apply_template, which this wrapper
// must get right:
//
//   - n >= 0 && n <= len(buf): success; n is the byte length written.
//   - n > len(buf): buffer too small; n is the required size (positive form).
//   - n < 0: either "need -n bytes" OR a hard failure (unsupported Jinja).
//     Empirically a failure is almost always -1 with a tiny |n| while the
//     prompt is clearly larger; a real size hint has -n >> len(prompt).
//     Treating a hard -1 as "need 1 byte" used to make us fall through to a
//     plain concatenation, which then decoded to empty output on chat models.
//
// When the GGUF's own Jinja fails (common for tool-calling templates the
// installed llama.cpp cannot evaluate), fall back to the builtin "chatml"
// name — that is exactly what the official yzma chat example does when the
// model template is empty.
func (g *generator) applyChatTemplate(system, user string) (string, bool) {
	plain := func() string {
		if strings.TrimSpace(system) == "" {
			return user
		}
		return system + "\n\n" + user
	}

	messages := make([]llama.ChatMessage, 0, 2)
	if strings.TrimSpace(system) != "" {
		messages = append(messages, llama.NewChatMessage("system", system))
	}
	messages = append(messages, llama.NewChatMessage("user", user))
	// NewChatMessage holds *byte into GC-managed NUL-terminated copies. Keep
	// the slice alive across the FFI call so those bytes are not collected.
	defer runtime.KeepAlive(messages)

	try := func(template string) (string, bool) {
		if strings.TrimSpace(template) == "" {
			return "", false
		}
		buf := make([]byte, 256*1024)
		n := llama.ChatApplyTemplate(template, messages, true, buf)
		if n < 0 {
			need := int(-n)
			// A hard failure is typically -1; a real "buffer too small" hint
			// is large. Only retry when the claimed size is plausible.
			if need <= len(buf) || need > 8*1024*1024 {
				return "", false
			}
			buf = make([]byte, need+1024)
			n = llama.ChatApplyTemplate(template, messages, true, buf)
			if n < 0 {
				return "", false
			}
		}
		if int(n) > len(buf) {
			buf = make([]byte, int(n)+1024)
			n = llama.ChatApplyTemplate(template, messages, true, buf)
			if n < 0 || int(n) > len(buf) {
				return "", false
			}
		}
		rendered := string(buf[:n])
		if strings.TrimSpace(rendered) == "" {
			return "", false
		}
		return rendered, true
	}

	if rendered, ok := try(llama.ModelChatTemplate(g.model, "")); ok {
		return rendered, true
	}
	// Official yzma chat fallback when the model template is missing or, in
	// our case, when its Jinja cannot be evaluated by this llama.cpp build.
	if rendered, ok := try("chatml"); ok {
		return rendered, true
	}
	return plain(), false
}

// Close frees the context and model. It is safe to call more than once.
func (g *generator) Close() error {
	if g == nil {
		return nil
	}
	// Lock order: runtimeMu first, then g.mu.
	LockRuntime()
	defer UnlockRuntime()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	g.freeModelLocked()
	return nil
}

// waitForReady triggers a load if needed and polls until ready, failed, or the
// deadline passes. Used by tests and settings preflight — not by process start.
func (g *generator) waitForReady(ctx context.Context, timeout time.Duration) error {
	if g == nil {
		return organizer.ErrUnavailable
	}
	go g.ensureLoaded()
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if g.Ready() {
			return nil
		}
		if err := g.LoadError(); err != nil {
			// Still loading may leave loadErr nil; only fail when load finished bad.
			g.mu.Lock()
			loading := g.loading
			g.mu.Unlock()
			if !loading {
				return err
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("yzma: model still loading after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

var _ organizer.LocalGenerator = (*generator)(nil)
