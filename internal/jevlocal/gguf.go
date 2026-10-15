package jevlocal

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/solosw/solcode/internal/organizer/yzma"
	"github.com/solosw/solcode/internal/systemone"
)

const decisionContext = 4096
const decisionBatch = 256

// GGUFEvaluator reads Kev's trained pointer head, never sampled text. All native
// calls and idle/peer unloads share the organizer/embedding runtime lock.
type GGUFEvaluator struct {
	// Timeout bounds a question request; zero defaults to 20 seconds.
	Timeout      time.Duration
	lastUsed     time.Time
	path         string
	model        llama.Model
	native       llama.Context
	vocab        llama.Vocab
	temperatures map[string]float64
	closed       bool
	idle         *time.Timer
}

func NewGGUF(path string) (*GGUFEvaluator, error) {
	if !strings.EqualFold(filepath.Ext(path), ".gguf") || !yzma.ModelPresent(path) {
		return nil, fmt.Errorf("jev: GGUF model not found: %s", path)
	}
	return &GGUFEvaluator{path: path}, nil
}
func (e *GGUFEvaluator) Backend() string  { return systemone.BackendLocal }
func (e *GGUFEvaluator) Model() string    { return filepath.Base(e.path) }
func (e *GGUFEvaluator) Configured() bool { return e != nil && e.path != "" }

// loadLocked is lazy: startup never maps the 4B weights. GPU failures are
// reported to the deterministic decision fallback, not retried on CPU.
func (e *GGUFEvaluator) loadLocked() error {
	if e.closed {
		return fmt.Errorf("jev gguf: closed")
	}
	if e.model != 0 {
		return nil
	}
	if err := yzma.EnsureRuntimeLocked(""); err != nil {
		return err
	}
	if !llama.SupportsGpuOffload() {
		return fmt.Errorf("jev gguf: GPU required but unavailable")
	}
	yzma.ReleaseOtherGPUHolders(e)
	mp := llama.ModelDefaultParams()
	mp.NGpuLayers = -1
	mp.LoadMode = llama.LoadModeMmap
	m, err := llama.ModelLoadFromFile(e.path, mp)
	if err != nil {
		return fmt.Errorf("jev gguf load: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = llama.ModelFree(m)
		}
	}()
	arch, _ := llama.ModelMetaValStr(m, "general.architecture")
	kind, _ := llama.ModelMetaValStr(m, arch+".decision.type")
	if kind != "kev" {
		return fmt.Errorf("jev gguf: unsupported decision type %q (requires Kev pointer head)", kind)
	}
	dim := llama.ModelNEmbdOut(m)
	if dim <= 0 || dim%2 != 0 {
		return fmt.Errorf("jev gguf: invalid pointer dimensions %d", dim)
	}
	temps := map[string]float64{}
	for _, t := range []string{"choice", "score", "noul"} {
		raw, found := llama.ModelMetaValStr(m, arch+".decision.temperature."+t)
		value, err := strconv.ParseFloat(raw, 64)
		if !found || err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("jev gguf: invalid %s calibration temperature %q", t, raw)
		}
		temps[t] = value
	}
	cp := llama.ContextDefaultParams()
	cp.NCtx = decisionContext
	cp.NBatch = decisionBatch
	cp.NUbatch = decisionBatch
	cp.NOutputsMax = decisionBatch
	cp.NOutputsMaxPerSeq = decisionBatch
	cp.Embeddings = 1
	cp.PoolingType = llama.PoolingTypeNone
	c, err := llama.InitFromModel(m, cp)
	if err != nil {
		return fmt.Errorf("jev gguf context: %w", err)
	}
	e.model, e.native, e.vocab, e.temperatures = m, c, llama.ModelGetVocab(m), temps
	ok = true
	yzma.RegisterGPUHolder(e, e.unloadLocked)
	return nil
}

func (e *GGUFEvaluator) unloadLocked() {
	if e.idle != nil {
		e.idle.Stop()
		e.idle = nil
	}
	if e.native != 0 {
		_ = llama.Free(e.native)
		e.native = 0
	}
	if e.model != 0 {
		_ = llama.ModelFree(e.model)
		e.model = 0
	}
	e.vocab = 0
	yzma.UnregisterGPUHolder(e)
}
func (e *GGUFEvaluator) Close() error {
	yzma.LockRuntime()
	defer yzma.UnlockRuntime()
	e.closed = true
	e.unloadLocked()
	return nil
}

var kevSpecial = regexp.MustCompile(`<\|([A-Za-z0-9_]+)\|>`)

func kevText(v any) string { return kevSpecial.ReplaceAllString(kevRender(v, 0), "<¦$1¦>") }

// kevRender follows Kev's training renderer: objects are labelled lines, not JSON.
func kevRender(v any, indent int) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	// Normalize structs and typed maps/slices without depending on reflection order.
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	return kevRenderValue(value, indent)
}
func kevRenderValue(v any, indent int) string {
	pad := strings.Repeat("  ", indent)
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case []any:
		lines := make([]string, 0, len(x))
		for _, item := range x {
			lines = append(lines, pad+"- "+strings.TrimLeft(kevRenderValue(item, indent+1), " \t\n\r"))
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, k := range keys {
			switch x[k].(type) {
			case []any, map[string]any:
				lines = append(lines, pad+k+":\n"+kevRenderValue(x[k], indent+1))
			default:
				lines = append(lines, pad+k+": "+kevRenderValue(x[k], 0))
			}
		}
		return strings.Join(lines, "\n")
	default:
		return fmt.Sprint(v)
	}
}

func kevPrompt(state any, p questionPlan) string {
	var b strings.Builder
	b.WriteString("<|fim_prefix|>" + kevText(state) + "<|fim_middle|>" + kevText(p.Instructions))
	for i, key := range p.Options {
		text := kevText(p.OptionTexts[i])
		if p.Type != systemone.TypeScore {
			text = kevText(key)
			if p.OptionTexts[i] != "" {
				text += ": " + kevText(p.OptionTexts[i])
			}
		}
		b.WriteString("<|box_start|>" + text + "<|box_end|>")
	}
	b.WriteString("<|fim_suffix|>")
	return b.String()
}

func (e *GGUFEvaluator) Ask(ctx context.Context, state any, questions map[string]systemone.Question) (systemone.Answers, systemone.Usage, error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if state == nil {
		return nil, systemone.Usage{}, fmt.Errorf("jev gguf: state required")
	}
	if _, err := json.Marshal(state); err != nil {
		return nil, systemone.Usage{}, fmt.Errorf("jev gguf state: %w", err)
	}
	if _, err := json.Marshal(questions); err != nil {
		return nil, systemone.Usage{}, fmt.Errorf("jev gguf questions: %w", err)
	}
	plans, err := planQuestions(questions)
	if err != nil {
		return nil, systemone.Usage{}, err
	}
	if len(plans) == 0 {
		return nil, systemone.Usage{}, fmt.Errorf("jev gguf: questions required")
	}
	// Render structured values exactly as Kev was trained, not fmt.Sprint/JSON.
	for i := range plans {
		p := &plans[i]
		q := questions[p.ID]
		p.Instructions = kevText(q.Instructions)
		if len(p.Options) > 255 {
			return nil, systemone.Usage{}, fmt.Errorf("jev gguf: at most 255 options")
		}
		if p.Type == systemone.TypeScore && (len(p.Options) < 2 || len(p.Options) > 10) {
			return nil, systemone.Usage{}, fmt.Errorf("jev gguf: score requires 2 to 10 levels")
		}
		raw, _ := json.Marshal(q.Criteria)
		if p.Type == systemone.TypeScore {
			var levels []any
			_ = json.Unmarshal(raw, &levels)
			for j, v := range levels {
				p.OptionTexts[j] = kevText(v)
			}
		} else {
			var desc map[string]any
			if string(raw) != "null" {
				if err := json.Unmarshal(raw, &desc); err != nil {
					return nil, systemone.Usage{}, fmt.Errorf("jev gguf: criteria must be an object")
				}
			}
			for j, k := range p.Options {
				if p.Type == systemone.TypeNoul {
					k = []string{"false", "true"}[j]
				}
				p.OptionTexts[j] = kevText(desc[k])
			}
		}
	}
	yzma.LockRuntime()
	defer yzma.UnlockRuntime()
	if err = ctx.Err(); err != nil {
		return nil, systemone.Usage{}, err
	}
	if err = e.loadLocked(); err != nil {
		return nil, systemone.Usage{}, err
	}
	if e.idle != nil {
		e.idle.Stop()
	}
	defer func() {
		e.lastUsed = time.Now()
		e.idle = time.AfterFunc(30*time.Second, func() {
			yzma.LockRuntime()
			defer yzma.UnlockRuntime()
			if time.Since(e.lastUsed) >= 30*time.Second {
				e.unloadLocked()
			}
		})
	}()
	answers := systemone.Answers{}
	usage := systemone.Usage{}
	for _, p := range plans {
		scores, n, err := e.scoresLocked(ctx, kevPrompt(state, p), len(p.Options))
		if err != nil {
			return nil, usage, err
		}
		decoded, err := DecodeLogits([]questionPlan{p}, scores, e.temperatures[p.Type])
		if err != nil {
			return nil, usage, err
		}
		a := decoded[p.ID]
		probs := softmaxTemp(scores, e.temperatures[p.Type])
		if p.Type == systemone.TypeChoice {
			a.Confidence = choiceConfidence(probs)
		}
		if p.Type == systemone.TypeScore {
			a.Confidence = scoreConfidence(probs)
			a.Probabilities = map[string]float64{}
			for i, p := range probs {
				a.Probabilities[strconv.Itoa(i)] = p
			}
		}
		answers[p.ID] = a
		usage.InputTokens += n
	}
	return answers, usage, nil
}

func choiceConfidence(probs []float64) float64 {
	if len(probs) < 2 {
		return 1
	}
	best := 0.0
	for _, p := range probs {
		best = math.Max(best, p)
	}
	uniform := 1 / float64(len(probs))
	return math.Max(0, (best-uniform)/(1-uniform))
}

func scoreConfidence(probs []float64) float64 {
	if len(probs) < 2 {
		return 1
	}
	mode := 0
	for i, p := range probs {
		if p > probs[mode] {
			mode = i
		}
	}
	dist, uniform := 0.0, 0.0
	for i, p := range probs {
		dist += p * math.Abs(float64(i-mode))
		uniform += math.Abs(float64(i)-float64(len(probs)-1)/2) / float64(len(probs))
	}
	return math.Max(0, 1-dist/uniform)
}

func (e *GGUFEvaluator) scoresLocked(ctx context.Context, prompt string, nOptions int) ([]float32, int, error) {
	tokens := llama.Tokenize(e.vocab, prompt, false, true)
	marker := llama.Tokenize(e.vocab, "<|box_end|>", false, true)
	if len(marker) != 1 {
		return nil, 0, fmt.Errorf("jev gguf: missing option marker")
	}
	if len(tokens) == 0 || len(tokens) > decisionContext {
		return nil, 0, fmt.Errorf("jev gguf: prompt has %d tokens (limit %d); refusing to drop options", len(tokens), decisionContext)
	}
	mem, err := llama.GetMemory(e.native)
	if err != nil {
		return nil, 0, err
	}
	if err := llama.MemoryClear(mem, true); err != nil {
		return nil, 0, err
	}
	dim := llama.ModelNEmbdOut(e.model)
	keys := [][]float32{}
	var query []float32
	batch := llama.BatchInit(decisionBatch, 0, 1)
	defer llama.BatchFree(batch)
	for start := 0; start < len(tokens); start += decisionBatch {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		_ = batch.Clear()
		end := min(start+decisionBatch, len(tokens))
		for i := start; i < end; i++ {
			if err := batch.Add(tokens[i], llama.Pos(i), []llama.SeqId{0}, tokens[i] == marker[0] || i == len(tokens)-1); err != nil {
				return nil, 0, err
			}
		}
		code, err := llama.Decode(e.native, batch)
		if err != nil || code != 0 {
			return nil, 0, fmt.Errorf("jev gguf decode: code=%d: %v", code, err)
		}
		for i := start; i < end; i++ {
			if tokens[i] != marker[0] && i != len(tokens)-1 {
				continue
			}
			emb, err := llama.GetEmbeddingsIth(e.native, int32(i-start), dim)
			if err != nil || len(emb) != int(dim) {
				return nil, 0, fmt.Errorf("jev gguf: pointer output unavailable: %v", err)
			}
			if tokens[i] == marker[0] {
				keys = append(keys, append([]float32(nil), emb[dim/2:]...))
			}
			if i == len(tokens)-1 {
				query = append([]float32(nil), emb[:dim/2]...)
			}
		}
	}
	if len(keys) != nOptions {
		return nil, 0, fmt.Errorf("jev gguf: option markers %d != %d", len(keys), nOptions)
	}
	scores := make([]float32, nOptions)
	for i, key := range keys {
		var dot float64
		for j, k := range key {
			dot += float64(query[j]) * float64(k)
		}
		scores[i] = float32(dot / math.Sqrt(float64(len(query))))
		if math.IsNaN(float64(scores[i])) || math.IsInf(float64(scores[i]), 0) {
			return nil, 0, fmt.Errorf("jev gguf: non-finite pointer score")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return scores, len(tokens), nil
}

var _ systemone.Evaluator = (*GGUFEvaluator)(nil)
