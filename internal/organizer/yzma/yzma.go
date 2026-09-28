package yzma

import (
	"context"
	"fmt"
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

		llama.BackendInit()

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

	prompt := g.applyChatTemplate(req.System, req.User)
	tokens := llama.Tokenize(g.vocab, prompt, true, true)
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

	batch := llama.BatchGetOne(tokens)
	for pos := 0; pos < maxTokens; pos++ {
		if err := ctx.Err(); err != nil {
			return out.String(), err
		}
		if _, err := llama.Decode(g.ctx, batch); err != nil {
			return out.String(), fmt.Errorf("yzma: decode: %w", err)
		}
		next := llama.SamplerSample(smpl, g.ctx, -1)
		if llama.VocabIsEOG(g.vocab, next) {
			break
		}
		if n := llama.TokenToPiece(g.vocab, next, pieceBuf, 0, false); n > 0 {
			out.Write(pieceBuf[:n])
		}
		llama.SamplerAccept(smpl, next)
		// Feed the sampled token back as the next single-token batch.
		batch = llama.BatchGetOne([]llama.Token{next})
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

// applyChatTemplate renders system+user through the model's own template.
//
// Using the GGUF's embedded template keeps the prompt shaped the way the model
// was trained. When the model ships no template the messages are concatenated,
// which still works for instruction-tuned models though less precisely.
func (g *generator) applyChatTemplate(system, user string) string {
	template := llama.ModelChatTemplate(g.model, "")
	if strings.TrimSpace(template) == "" {
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

	// ChatApplyTemplate reports the required buffer size when the buffer is too
	// small, so size it from the returned length on the first pass.
	buf := make([]byte, 4096)
	if n := llama.ChatApplyTemplate(template, messages, true, buf); int(n) > len(buf) {
		buf = make([]byte, n)
		n = llama.ChatApplyTemplate(template, messages, true, buf)
		if int(n) > len(buf) {
			return system + "\n\n" + user
		}
	}
	rendered := strings.TrimRight(string(buf), "\x00")
	if strings.TrimSpace(rendered) == "" {
		return system + "\n\n" + user
	}
	return rendered
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
