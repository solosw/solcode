// Package systemone is a small client for TypeSafe System One models (Jev).
//
// System One models do not generate text. They evaluate a state and return
// typed, constrained answers — a Choice from a fixed option set, a Score along
// ordered levels, or a Noul (the probability a yes/no statement is true) — plus
// a calibrated confidence. That makes them useful as a decision layer inside an
// agent loop (routing, gating, classification) rather than as the model that
// writes the code.
//
// The zero value is not usable; build a Client with NewClient. Nothing in this
// package is required for solcode to run: when Jev is disabled or unreachable,
// callers must fall back to their existing deterministic behavior. See Safe.
package systemone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/solosw/solcode/internal/httpproxy"
)

// Question types accepted by the evaluation endpoint.
const (
	TypeChoice = "choice"
	TypeScore  = "score"
	TypeNoul   = "noul"
)

const (
	// DefaultBaseURL is the hosted TypeSafe evaluation endpoint origin.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the SDK default alias.
	DefaultModel = "jev-latest"

	defaultTimeoutSec = 20
	maxTimeoutSec     = 120
	// maxResponseBytes caps the response body we are willing to read.
	maxResponseBytes = 4 << 20
	// maxCacheEntries bounds the in-process answer cache.
	maxCacheEntries = 256
	// maxChoiceOptions is the documented per-question option ceiling.
	maxChoiceOptions = 255
)

// Question is one typed judgment to evaluate against the state.
type Question struct {
	Type string `json:"type"`
	// Instructions is the question text. A string is enough for most callers;
	// an object or array lets code attach the data the question refers to.
	Instructions any `json:"instructions"`
	// Criteria defines the answer space: a map of option -> description for
	// Choice, an ordered level list for Score, and optional yes/no wording for
	// Noul. Omitted for a bare Noul.
	Criteria any `json:"criteria,omitempty"`
}

// Choice builds a Choice question. Option names are what callers switch on;
// descriptions are what separates one option from another for the model.
func Choice(instructions string, options map[string]string) Question {
	criteria := make(map[string]any, len(options))
	for name, desc := range options {
		criteria[name] = desc
	}
	return Question{Type: TypeChoice, Instructions: instructions, Criteria: criteria}
}

// ChoiceKeys builds a Choice question whose options are the supplied names,
// using a caller-provided description for each. Order of keys is irrelevant.
func ChoiceKeys(instructions string, options []string, describe func(string) string) Question {
	criteria := make(map[string]any, len(options))
	for _, name := range options {
		desc := ""
		if describe != nil {
			desc = describe(name)
		}
		criteria[name] = desc
	}
	return Question{Type: TypeChoice, Instructions: instructions, Criteria: criteria}
}

// Noul builds a yes/no question.
func Noul(instructions string) Question {
	return Question{Type: TypeNoul, Instructions: instructions}
}

// NoulWithCriteria builds a yes/no question that spells out what counts as a
// yes and what counts as a no. Use it when the boundary is subtle.
func NoulWithCriteria(instructions, yes, no string) Question {
	return Question{
		Type:         TypeNoul,
		Instructions: instructions,
		Criteria:     map[string]any{"true": yes, "false": no},
	}
}

// Score builds a Score question over ordered levels (lowest first).
func Score(instructions string, levels []string) Question {
	criteria := make([]any, 0, len(levels))
	for _, level := range levels {
		criteria = append(criteria, level)
	}
	return Question{Type: TypeScore, Instructions: instructions, Criteria: criteria}
}

// Answer is one typed response, keyed by the question id from the request.
//
// Only the fields relevant to the answer's Type are populated: Choice sets
// Choice/Probabilities/Confidence, Score sets Score/Legend/Confidence, and Noul
// sets Noul only (a Noul has no separate confidence — the probability is the
// answer and the certainty in one).
type Answer struct {
	Type          string
	Choice        string
	Confidence    float64
	Probabilities map[string]float64
	Noul          float64
	Score         float64
	Legend        []string
}

// Answers maps question id to answer.
type Answers map[string]Answer

// Usage reports token accounting for one evaluation.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Options configures the Jev client.
type Options struct {
	// BaseURL is the API origin, e.g. https://api.typesafe.ai. An origin that
	// already ends in /v1 is accepted.
	BaseURL string
	APIKey  string
	// Model selects the System One model. Empty uses DefaultModel.
	Model string
	// TimeoutSec bounds a single evaluation (default 20, max 120).
	TimeoutSec int
	// HTTPClient overrides the transport; nil uses the proxy-aware default.
	HTTPClient *http.Client
	// DisableCache turns off the in-process answer cache (mainly for tests).
	DisableCache bool
}

// Client evaluates questions against a state.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client

	cacheMu sync.Mutex
	cache   map[string]cacheEntry
	cacheQ  []string
	noCache bool
}

type cacheEntry struct {
	answers Answers
	usage   Usage
	// expiresAt bounds how long a decision may be reused. Decisions are cheap
	// to recompute and the underlying state changes, so entries are short-lived.
	expiresAt time.Time
}

const cacheTTL = 2 * time.Minute

// NewClient builds a client. It never returns nil; an empty BaseURL falls back
// to the hosted endpoint so a misconfigured value fails loudly at call time
// rather than silently disabling the feature.
func NewClient(opts Options) *Client {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = DefaultModel
	}
	timeout := opts.TimeoutSec
	if timeout <= 0 {
		timeout = defaultTimeoutSec
	}
	if timeout > maxTimeoutSec {
		timeout = maxTimeoutSec
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = httpproxy.NewClient(time.Duration(timeout) * time.Second)
	}
	c := &Client{
		baseURL: base,
		apiKey:  strings.TrimSpace(opts.APIKey),
		model:   model,
		http:    httpClient,
		cache:   make(map[string]cacheEntry),
		noCache: opts.DisableCache,
	}
	return c
}

func (c *Client) Model() string { return c.model }

// Configured reports whether the client has the credentials needed to call out.
func (c *Client) Configured() bool {
	return c != nil && c.apiKey != ""
}

func (c *Client) endpoint() string {
	if strings.HasSuffix(c.baseURL, "/v1") {
		return c.baseURL + "/systemone"
	}
	return c.baseURL + "/v1/systemone"
}

type askRequest struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

type rawAnswerEnvelope struct {
	Model string `json:"model"`
	// Answers is decoded per-question so a Score's array-shaped probabilities
	// cannot break a Choice answer in the same response.
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   Usage                      `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Ask evaluates questions against state. It returns an error rather than a
// partial result; callers that must not fail should go through Safe.
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (Answers, Usage, error) {
	if c == nil {
		return nil, Usage{}, fmt.Errorf("systemone client is nil")
	}
	if len(questions) == 0 {
		return Answers{}, Usage{}, nil
	}
	if !c.Configured() {
		return nil, Usage{}, fmt.Errorf("systemone api key is not configured")
	}
	if err := validateQuestions(questions); err != nil {
		return nil, Usage{}, err
	}

	body, err := json.Marshal(askRequest{State: state, Model: c.model, Questions: questions})
	if err != nil {
		return nil, Usage{}, fmt.Errorf("encode systemone request: %w", err)
	}

	key := c.cacheKey(body)
	if cached, ok := c.cached(key); ok {
		return cached.answers, cached.usage, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, Usage{}, fmt.Errorf("build systemone request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("call systemone: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, Usage{}, fmt.Errorf("read systemone response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, Usage{}, fmt.Errorf("systemone http %d: %s", resp.StatusCode, compactErrorBody(raw))
	}

	var envelope rawAnswerEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, Usage{}, fmt.Errorf("decode systemone response: %w", err)
	}
	if envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" {
		return nil, Usage{}, fmt.Errorf("systemone error: %s", envelope.Error.Message)
	}
	answers, err := decodeAnswers(envelope.Answers)
	if err != nil {
		return nil, Usage{}, err
	}
	c.store(key, cacheEntry{answers: answers, usage: envelope.Usage, expiresAt: time.Now().Add(cacheTTL)})
	return answers, envelope.Usage, nil
}

func validateQuestions(questions map[string]Question) error {
	for id, q := range questions {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("systemone question id must not be empty")
		}
		switch q.Type {
		case TypeChoice:
			if n := choiceOptionCount(q.Criteria); n == 0 {
				return fmt.Errorf("systemone choice question %q requires criteria", id)
			} else if n > maxChoiceOptions {
				return fmt.Errorf("systemone choice question %q has %d options (max %d)", id, n, maxChoiceOptions)
			}
		case TypeScore:
			if scoreLevelCount(q.Criteria) == 0 {
				return fmt.Errorf("systemone score question %q requires criteria levels", id)
			}
		case TypeNoul:
			// criteria is optional for Noul.
		default:
			return fmt.Errorf("systemone question %q has unsupported type %q", id, q.Type)
		}
		if instructionsEmpty(q.Instructions) {
			return fmt.Errorf("systemone question %q requires instructions", id)
		}
	}
	return nil
}

func choiceOptionCount(criteria any) int {
	switch typed := criteria.(type) {
	case map[string]any:
		return len(typed)
	case map[string]string:
		return len(typed)
	default:
		return 0
	}
}

func scoreLevelCount(criteria any) int {
	switch typed := criteria.(type) {
	case []any:
		return len(typed)
	case []string:
		return len(typed)
	default:
		return 0
	}
}

func instructionsEmpty(instructions any) bool {
	switch typed := instructions.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	default:
		return false
	}
}

func decodeAnswers(raw map[string]json.RawMessage) (Answers, error) {
	out := make(Answers, len(raw))
	for id, payload := range raw {
		var decoded struct {
			Type          string          `json:"type"`
			Choice        string          `json:"choice"`
			Confidence    float64         `json:"confidence"`
			Noul          float64         `json:"noul"`
			Score         float64         `json:"score"`
			Legend        []string        `json:"legend"`
			Probabilities json.RawMessage `json:"probabilities"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return nil, fmt.Errorf("decode systemone answer %q: %w", id, err)
		}
		answer := Answer{
			Type:       decoded.Type,
			Choice:     decoded.Choice,
			Confidence: decoded.Confidence,
			Noul:       decoded.Noul,
			Score:      decoded.Score,
			Legend:     decoded.Legend,
		}
		if len(decoded.Probabilities) > 0 {
			// Object-shaped for Choice; a Score may send an array. A shape we do
			// not understand is skipped rather than failing the whole response.
			var dist map[string]float64
			if json.Unmarshal(decoded.Probabilities, &dist) == nil && len(dist) > 0 {
				answer.Probabilities = dist
			}
		}
		out[id] = answer
	}
	return out, nil
}

func compactErrorBody(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

func (c *Client) cacheKey(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (c *Client) cached(key string) (cacheEntry, bool) {
	if c.noCache {
		return cacheEntry{}, false
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, ok := c.cache[key]
	if !ok {
		return cacheEntry{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.cache, key)
		return cacheEntry{}, false
	}
	return entry, true
}

func (c *Client) store(key string, entry cacheEntry) {
	if c.noCache {
		return
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if _, exists := c.cache[key]; !exists {
		c.cacheQ = append(c.cacheQ, key)
	}
	c.cache[key] = entry
	for len(c.cacheQ) > maxCacheEntries {
		oldest := c.cacheQ[0]
		c.cacheQ = c.cacheQ[1:]
		delete(c.cache, oldest)
	}
}

// Candidate is one option in a routing decision.
type Candidate struct {
	// Name is the identifier the caller switches on (skill or tool name).
	Name string
	// Description explains what the candidate does, for the model.
	Description string
}

// Ranked is one scored candidate, ordered by probability descending.
type Ranked struct {
	Name         string
	Probability  float64
	Confidence   float64
	Distribution map[string]float64
}

// Rank asks Jev which candidate best fits state, and returns every candidate
// ordered by probability so the caller can take the top N or apply its own
// threshold. This is the Choice-based reranking pattern: put the real options
// in one question rather than scoring candidates one at a time.
func (c *Client) Rank(ctx context.Context, state any, instructions string, candidates []Candidate, topN int) ([]Ranked, error) {
	return rank(ctx, c, state, instructions, candidates, topN)
}

// SingleChoice asks one Choice question and returns the selected option.
func (c *Client) SingleChoice(ctx context.Context, state any, instructions string, options map[string]string) (Answer, error) {
	return singleChoice(ctx, c, state, instructions, options)
}

// SingleNoul asks one yes/no question and returns the probability of yes.
func (c *Client) SingleNoul(ctx context.Context, state any, instructions string) (Answer, error) {
	return singleNoul(ctx, c, state, instructions)
}
