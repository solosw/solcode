// Package organizer implements the local memory-organizer model: a fully local
// text-generation model that turns a finished session into a structured summary
// plus candidate long-term memories.
//
// The package splits cleanly into two halves:
//
//   - This file defines the runtime seam (LocalGenerator) and the Organizer,
//     which owns prompt construction, structured parsing, and validation. It
//     never touches a native library, so it is fully testable with a fake.
//   - The runtime subpackage loads llama.cpp in-process via purego FFI and
//     implements LocalGenerator.
//
// Nothing here calls a remote or hosted endpoint, and there is no fallback to
// the chat provider: when the local model is unavailable the Organizer reports
// that it is unavailable rather than producing memory from a network model.
package organizer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUnavailable reports that the local model cannot run yet. Callers should
// treat it as a retryable condition, never as a cue to fall back to a remote
// model.
var ErrUnavailable = errors.New("local organizer model is unavailable")

// GenerateRequest is one text-generation request handed to a LocalGenerator.
type GenerateRequest struct {
	// System is the instruction preamble.
	System string
	// User is the state/transcript payload.
	User string
	// Grammar is an optional GBNF grammar that constrains decoding to valid
	// output. Empty means unconstrained generation.
	Grammar string
	// MaxTokens caps the completion length.
	MaxTokens int
	// Temperature controls sampling. Zero selects greedy decoding.
	Temperature float64
}

// LocalGenerator is the minimal seam over a local text-generation runtime.
//
// Implementations are expected to load lazily and report readiness through
// Ready, because loading a GGUF model can take seconds to tens of seconds and
// must not block process startup.
type LocalGenerator interface {
	// Name identifies the runtime for logs and diagnostics.
	Name() string
	// Ready reports whether Generate can currently succeed.
	Ready() bool
	// Generate runs one completion.
	Generate(ctx context.Context, req GenerateRequest) (string, error)
	// Close releases model/context resources. It must be safe to call twice.
	Close() error
}

// Kind/Scope/Tier mirror the memory package vocabulary. They are duplicated as
// plain strings so this package does not depend on internal/memory, which lets
// memory import organizer without a cycle.
type CandidateKind string

const (
	CandidateFact       CandidateKind = "fact"
	CandidatePreference CandidateKind = "preference"
	CandidateConstraint CandidateKind = "constraint"
	CandidateTask       CandidateKind = "task"
	CandidateWorkflow   CandidateKind = "workflow"
)

// CandidateTier is the suggested memory tier (M1..M5).
type CandidateTier string

const (
	CandidateTierSensory    CandidateTier = "M1"
	CandidateTierWorking    CandidateTier = "M2"
	CandidateTierShortTerm  CandidateTier = "M3"
	CandidateTierLongTerm   CandidateTier = "M4"
	CandidateTierProcedural CandidateTier = "M5"
)

// Input is one session handed to the organizer.
type Input struct {
	SessionID string
	WorkDir   string
	// Transcript is the session text to summarize (chat and/or tool trace).
	Transcript string
	// PreviousSummary carries the prior summary so a refresh does not lose
	// context that already left the message window.
	PreviousSummary string
	// NextSummary is the post-compact working summary when available.
	NextSummary string
	// Trigger labels the call site ("turn", "compact", …) for the model.
	Trigger string
	// ChangedFiles are checkpoint-captured paths for this session.
	ChangedFiles []string
	// Todos are open / in-progress task lines from the session todolist.
	Todos []string
	// ToolFacts are deterministic bullets (edits, validation commands, tools).
	ToolFacts []string
	// RelatedMemories are short existing archival snippets (id + text) so the
	// model can supersede or avoid duplicating known rules.
	RelatedMemories []string
}

// Candidate is one proposed long-term memory.
type Candidate struct {
	Kind       CandidateKind  `json:"kind"`
	Scope      string         `json:"scope"`
	Tier       CandidateTier `json:"suggested_tier"`
	Confidence float64        `json:"confidence"`
	Text       string         `json:"canonical_text"`
	Tags       []string       `json:"tags"`
	Reason     string         `json:"reason"`
	// Status is an optional governance hint (active|superseded|expired|contradicted).
	// Empty/unknown normalizes to active at parse time.
	Status string `json:"status,omitempty"`
	// Supersedes is an optional older memory id or short topic key this candidate replaces.
	Supersedes string `json:"supersedes,omitempty"`
}

// Result is one organizer outcome.
type Result struct {
	// Summary is the session summary written to the session record.
	Summary string
	// Keywords index the session memory entry.
	Keywords []string
	// Importance scores the session memory entry.
	Importance float64
	// Candidates are proposed long-term memories, already validated.
	Candidates []Candidate
	// Model identifies the model that produced the result.
	Model string
	// Elapsed is how long generation took.
	Elapsed time.Duration
}

// Organizer turns sessions into summaries and candidate memories using a local
// generation model.
type Organizer struct {
	gen LocalGenerator

	maxOutputTokens int
	temperature     float64
	// timeout bounds one generation. Zero means no organizer-imposed deadline.
	timeout time.Duration
	// maxCandidates caps how many candidates a single run may store, so a
	// confused model cannot flood the store.
	maxCandidates int
}

// Options configures an Organizer.
type Options struct {
	MaxOutputTokens int
	Temperature     float64
	TimeoutSec      int
	// MaxCandidates defaults to 12.
	MaxCandidates int
}

// New builds an Organizer around a generator. A nil generator yields a
// disabled Organizer whose Organize returns ErrUnavailable.
func New(gen LocalGenerator, opts Options) *Organizer {
	o := &Organizer{
		gen:             gen,
		maxOutputTokens: opts.MaxOutputTokens,
		temperature:     opts.Temperature,
		maxCandidates:   opts.MaxCandidates,
	}
	if o.maxOutputTokens <= 0 {
		// ~800 tokens is enough for summary + a handful of XML candidates
		// without eating most of a 4k context window.
		o.maxOutputTokens = 800
	}
	if o.maxCandidates <= 0 {
		o.maxCandidates = 12
	}
	if opts.TimeoutSec > 0 {
		o.timeout = time.Duration(opts.TimeoutSec) * time.Second
	}
	return o
}

// Available reports whether a generator is configured. Ready() is not required:
// yzma loads the GGUF on first Generate, so a cold organizer is still usable.
func (o *Organizer) Available() bool {
	return o != nil && o.gen != nil
}

// Generator returns the underlying generator, or nil when disabled.
func (o *Organizer) Generator() LocalGenerator {
	if o == nil {
		return nil
	}
	return o.gen
}

// Close releases the underlying generator.
func (o *Organizer) Close() error {
	if o == nil || o.gen == nil {
		return nil
	}
	return o.gen.Close()
}

// Organize runs one session through the local model and returns a validated
// summary plus candidate memories.
func (o *Organizer) Organize(ctx context.Context, input Input) (Result, error) {
	if o == nil || o.gen == nil {
		return Result{}, ErrUnavailable
	}
	// Do not gate on Ready(): demand-loaded generators start cold and load inside
	// Generate. A hard Ready check made every first Organize fail after idle unload.
	transcript := strings.TrimSpace(input.Transcript)
	if transcript == "" {
		return Result{}, fmt.Errorf("organizer: transcript is empty")
	}
	transcript = truncateRunes(transcript, maxTranscriptRunes)

	runCtx := ctx
	if o.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}

	start := time.Now()
	raw, err := o.gen.Generate(runCtx, GenerateRequest{
		System:      organizeSystemPrompt,
		User:        buildUserPayload(input, transcript),
		Grammar:     OrganizeGrammar(),
		MaxTokens:   o.maxOutputTokens,
		Temperature: o.temperature,
	})
	if err != nil {
		return Result{}, fmt.Errorf("organizer generate: %w", err)
	}

	parsed, err := ParseResult(raw)
	if err != nil {
		return Result{}, err
	}
	parsed.Model = o.gen.Name()
	parsed.Elapsed = time.Since(start)
	if len(parsed.Candidates) > o.maxCandidates {
		parsed.Candidates = parsed.Candidates[:o.maxCandidates]
	}
	return parsed, nil
}
