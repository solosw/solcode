package embedding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/jevlocal"
	sp "github.com/tggo/goSentencePiece"
)

const (
	defaultLocalMaxLen = 2048
	defaultQueryPrefix = "task: search result | query: "
	defaultDocPrefix   = "title: none | text: "
)

// LocalArtifacts describes a resolved EmbeddingGemma-style ONNX layout.
type LocalArtifacts struct {
	Dir      string
	ONNXPath string
	ModelID  string
	MaxLen   int
}

type localProvider struct {
	arts         LocalArtifacts
	dimensions   int
	maxLen       int
	queryPref    string
	docPref      string
	ortLib       string
	gpu          bool
	cudaDeviceID int

	mu      sync.Mutex
	tok     *sp.Tokenizer
	eng     *jevlocal.ORTEngine
	ready   bool
	loadErr error
	closed  bool
	once    sync.Once
}

func newLocalProvider(opts Options) (*localProvider, error) {
	cfg := opts.Config
	modelID := strings.TrimSpace(cfg.Model)
	if modelID == "" {
		return nil, fmt.Errorf("embedding local: model is required")
	}
	arts, err := ResolveLocalArtifacts(opts.ModelDir, cfg.Dir, modelID)
	if err != nil {
		return nil, err
	}
	p := &localProvider{
		arts:         arts,
		dimensions:   cfg.Dimensions,
		maxLen:       arts.MaxLen,
		queryPref:    defaultQueryPrefix,
		docPref:      defaultDocPrefix,
		ortLib:       strings.TrimSpace(opts.ORTLib),
		gpu:          opts.GPU,
		cudaDeviceID: opts.CudaDeviceID,
	}
	go p.ensureLoaded()
	return p, nil
}

// ResolveLocalArtifacts finds model_*.onnx (+ tokenizer) under candidates.
// Order: explicit modelDir, project Dir, SharedEmbeddingModelDir.
func ResolveLocalArtifacts(modelDir, projectDir, modelID string) (LocalArtifacts, error) {
	candidates := uniqueDirs(
		strings.TrimSpace(modelDir),
		strings.TrimSpace(projectDir),
		config.SharedEmbeddingModelDir(),
	)
	var lastErr error
	for _, dir := range candidates {
		arts, err := inspectLocalDir(dir, modelID)
		if err == nil {
			return arts, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("embedding local: no model directories to search")
	}
	return LocalArtifacts{}, lastErr
}

func inspectLocalDir(dir, modelID string) (LocalArtifacts, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return LocalArtifacts{}, fmt.Errorf("embedding model dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return LocalArtifacts{}, fmt.Errorf("embedding model dir is not a directory: %s", dir)
	}
	onnx := firstExisting(
		filepath.Join(dir, "model_q4f16.onnx"),
		filepath.Join(dir, "model_q4.onnx"),
		filepath.Join(dir, "model.onnx"),
		filepath.Join(dir, "onnx", "model_q4f16.onnx"),
		filepath.Join(dir, "onnx", "model_q4.onnx"),
		filepath.Join(dir, "onnx", "model.onnx"),
	)
	if onnx == "" {
		return LocalArtifacts{}, fmt.Errorf("embedding model dir %s: missing model_*.onnx", dir)
	}
	if !fileExists(filepath.Join(dir, "tokenizer.model")) && !fileExists(filepath.Join(dir, "tokenizer.json")) {
		return LocalArtifacts{}, fmt.Errorf("embedding model dir %s: need tokenizer.model or tokenizer.json", dir)
	}
	id := strings.TrimSpace(modelID)
	if id == "" {
		id = filepath.Base(dir)
	}
	return LocalArtifacts{
		Dir:      dir,
		ONNXPath: onnx,
		ModelID:  id,
		MaxLen:   defaultLocalMaxLen,
	}, nil
}

func (p *localProvider) ensureLoaded() {
	p.once.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed {
			p.loadErr = fmt.Errorf("embedding local: closed")
			return
		}
		tok, err := loadLocalTokenizer(p.arts.Dir)
		if err != nil {
			p.loadErr = err
			return
		}
		lib, err := jevlocal.EnsureORTLibrary(p.ortLib)
		if err != nil {
			p.loadErr = fmt.Errorf("embedding local ort: %w", err)
			return
		}
		eng, err := jevlocal.NewORTEngine(jevlocal.ORTOptions{
			ModelPath:     p.arts.ONNXPath,
			SharedLibrary: lib,
			GPU:           p.gpu,
			CudaDeviceID:  p.cudaDeviceID,
		})
		if err != nil {
			p.loadErr = fmt.Errorf("embedding local ort: %w", err)
			return
		}
		p.tok = tok
		p.eng = eng
		p.ready = true
	})
}

func loadLocalTokenizer(dir string) (*sp.Tokenizer, error) {
	spm := filepath.Join(dir, "tokenizer.model")
	if fileExists(spm) {
		tok, err := sp.NewTokenizer(spm)
		if err != nil {
			return nil, fmt.Errorf("load tokenizer.model: %w", err)
		}
		return tok, nil
	}
	jsonPath := filepath.Join(dir, "tokenizer.json")
	if fileExists(jsonPath) {
		tok, err := sp.NewTokenizerFromJSON(jsonPath)
		if err != nil {
			return nil, fmt.Errorf("load tokenizer.json: %w", err)
		}
		return tok, nil
	}
	return nil, fmt.Errorf("tokenizer.model or tokenizer.json missing in %s", dir)
}

func (p *localProvider) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(timeoutFrom(config.EmbeddingConfig{TimeoutSec: 120}))
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
			return fmt.Errorf("embedding local: closed")
		}
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("embedding local: engine not ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (p *localProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.waitReady(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.eng == nil || p.tok == nil {
		return nil, fmt.Errorf("embedding local: not ready")
	}

	return p.embedPrefixed(ctx, p.queryPref+text)
}

func (p *localProvider) EmbedDocument(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.waitReady(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.eng == nil || p.tok == nil {
		return nil, fmt.Errorf("embedding local: not ready")
	}
	return p.embedPrefixed(ctx, p.docPref+text)
}

func (p *localProvider) embedPrefixed(ctx context.Context, prefixed string) ([]float32, error) {
	ids, err := p.tok.Encode(prefixed)
	if err != nil {
		return nil, fmt.Errorf("tokenize: %w", err)
	}
	seq := make([]int64, 0, len(ids)+2)
	seq = append(seq, 2) // <bos>
	for _, id := range ids {
		seq = append(seq, int64(id))
	}
	seq = append(seq, 1) // <eos>
	if p.maxLen > 0 && len(seq) > p.maxLen {
		seq = seq[:p.maxLen]
		seq[len(seq)-1] = 1
	}
	mask := make([]int64, len(seq))
	for i := range mask {
		mask[i] = 1
	}

	inputs := jevlocal.NamedTensors{
		Int64: map[string]jevlocal.Int64Tensor{
			"input_ids":      {Shape: []int64{1, int64(len(seq))}, Data: seq},
			"attention_mask": {Shape: []int64{1, int64(len(mask))}, Data: mask},
		},
	}
	outs, err := p.eng.RunNamed(ctx, inputs)
	if err != nil {
		return nil, err
	}
	vec := outs.Float32["sentence_embedding"].Data
	if len(vec) == 0 {
		if lhs := outs.Float32["last_hidden_state"]; len(lhs.Data) > 0 && len(lhs.Shape) == 3 {
			vec = meanPool(lhs.Data, int(lhs.Shape[1]), int(lhs.Shape[2]), mask)
		}
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("embedding local: empty sentence_embedding")
	}
	return truncateAndNormalize(vec, p.dimensions), nil
}

func (p *localProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	var err error
	if p.eng != nil {
		err = p.eng.Close()
		p.eng = nil
	}
	p.tok = nil
	p.ready = false
	return err
}

func meanPool(data []float32, seq, hidden int, mask []int64) []float32 {
	if seq*hidden != len(data) || hidden == 0 {
		return nil
	}
	out := make([]float32, hidden)
	var n float32
	for t := 0; t < seq; t++ {
		if t < len(mask) && mask[t] == 0 {
			continue
		}
		n++
		offset := t * hidden
		for d := 0; d < hidden; d++ {
			out[d] += data[offset+d]
		}
	}
	if n == 0 {
		return out
	}
	for i := range out {
		out[i] /= n
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func uniqueDirs(dirs ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		key := filepath.Clean(d)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

var (
	_ Provider         = (*localProvider)(nil)
	_ DocumentEmbedder = (*localProvider)(nil)
)
