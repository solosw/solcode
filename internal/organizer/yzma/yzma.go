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
}

const (
	defaultContextSize = 8192
	// pieceBufferSize is the scratch buffer for one detokenized piece.
	pieceBufferSize = 512
)

// generator implements organizer.LocalGenerator on top of llama.cpp.
//
// Loading is deferred to a background goroutine: a multi-gigabyte GGUF can take
// many seconds to map, and solcode must not block startup on it. Until loading
// finishes, Ready reports false and Generate returns an unavailable error, which
// the worker treats as retryable.
type generator struct {
	cfg Config

	mu      sync.Mutex
	model   llama.Model
	ctx     llama.Context
	vocab   llama.Vocab
	ready   bool
	loadErr error
	closed  bool
	once    sync.Once
}

// New builds a generator and starts loading in the background.
//
// It returns a generator that reports unavailability through Ready when the
// model or library cannot be loaded, rather than failing at construction, so
// solcode keeps running and the caller can retry.
func New(cfg Config) organizer.LocalGenerator {
	g := &generator{cfg: cfg}
	go g.ensureLoaded()
	return g
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

func (g *generator) ensureLoaded() {
	g.once.Do(func() {
		modelPath := strings.TrimSpace(g.cfg.ModelPath)
		if modelPath == "" {
			g.setLoadErr(fmt.Errorf("yzma: model path is empty"))
			return
		}
		if !ModelPresent(modelPath) {
			g.setLoadErr(fmt.Errorf("yzma: GGUF model not found at %s", modelPath))
			return
		}
		libDir := ResolveLibraryDir(g.cfg.LibDir)
		var bindErr error
		libBindOnce.Do(func() {
			bindErr = loadLibrary(libDir)
		})
		if bindErr != nil {
			g.setLoadErr(bindErr)
			return
		}
		if !LibraryPresent(libDir) {
			// Another generator already bound a different dir, or the library
			// disappeared. Report rather than proceed into a nil-symbol call.
			g.setLoadErr(fmt.Errorf("yzma: llama.cpp shared library not available in %s", libDir))
			return
		}

		// Backends must be registered before any model is loaded. llama.Load
		// only binds symbols; without this call ModelLoadFromFile fails with
		// "no backends are loaded". Loading is process-global, so it runs once.
		backendOnce.Do(func() {
			// Silence llama.cpp / ggml stdout (model load dumps, CUDA graph
			// "id N reused" spam). The organizer runs inside solcode's process;
			// those lines otherwise flood the TUI and live-test output. Match
			// the official yzma chat example's non-verbose path.
			llama.LogSet(llama.LogSilent())
			llama.BackendInit()
			// Register the ggml backends shipped next to llama.dll. The
			// default search path does not cover the install directory, so the
			// explicit path form is required.
			if err := llama.GGMLBackendLoadAllFromPath(libDir); err != nil {
				backendErr = fmt.Errorf("yzma: load ggml backends from %s: %w", libDir, err)
			}
		})
		if backendErr != nil {
			g.setLoadErr(backendErr)
			return
		}

		modelParams := llama.ModelDefaultParams()
		if g.cfg.GPULayers == 0 {
			modelParams.SetCPUOnly()
		} else {
			modelParams.NGpuLayers = int32(g.cfg.GPULayers)
		}
		model, err := llama.ModelLoadFromFile(modelPath, modelParams)
		if err != nil {
			g.setLoadErr(fmt.Errorf("yzma: load GGUF %s: %w", modelPath, err))
			return
		}

		ctxParams := llama.ContextDefaultParams()
		contextSize := g.cfg.ContextSize
		if contextSize <= 0 {
			contextSize = defaultContextSize
		}
		ctxParams.NCtx = uint32(contextSize)
		ctxParams.NBatch = uint32(contextSize)
		if g.cfg.Threads > 0 {
			ctxParams.NThreads = int32(g.cfg.Threads)
			ctxParams.NThreadsBatch = int32(g.cfg.Threads)
		}
		modelCtx, err := llama.InitFromModel(model, ctxParams)
		if err != nil {
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
	})
}

func (g *generator) setLoadErr(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.loadErr == nil {
		g.loadErr = err
	}
}

// LoadError returns the deferred load failure, if any.
func (g *generator) LoadError() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.loadErr
}

// Generate runs one completion with the model held under a mutex.
//
// A single llama.cpp context is not safe for concurrent decoding, and the
// organizer deliberately runs one worker, so serializing here is both correct
// and sufficient.
func (g *generator) Generate(ctx context.Context, req organizer.GenerateRequest) (string, error) {
	if g == nil {
		return "", organizer.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
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
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	modelCtx := g.ctx
	model := g.model
	g.ctx = 0
	g.model = 0
	g.vocab = 0
	g.ready = false
	g.mu.Unlock()

	var firstErr error
	if modelCtx != 0 {
		if err := llama.Free(modelCtx); err != nil {
			firstErr = err
		}
	}
	if model != 0 {
		if err := llama.ModelFree(model); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// waitForReady polls until the generator is ready, failed, or the deadline
// passes. It exists for callers that need a synchronous answer, such as tests
// and an explicit preflight from the settings UI.
func (g *generator) waitForReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if g.Ready() {
			return nil
		}
		if err := g.LoadError(); err != nil {
			return err
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
