package organizer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGenerator is a LocalGenerator with scripted output. It lets the whole
// organizer path be tested without a native library or a GGUF model.
type fakeGenerator struct {
	name     string
	ready    bool
	raw      string
	err      error
	requests []GenerateRequest
	mu       sync.Mutex
	closed   bool
	// delay simulates slow local inference so timeouts can be exercised.
	delay time.Duration
}

func (f *fakeGenerator) Name() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
}

func (f *fakeGenerator) Ready() bool { return f.ready }

func (f *fakeGenerator) Generate(ctx context.Context, req GenerateRequest) (string, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	delay := f.delay
	err := f.err
	raw := f.raw
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
	if err != nil {
		return "", err
	}
	return raw, nil
}

func (f *fakeGenerator) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeGenerator) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

const validResponse = `{
  "session_summary": "Implemented the local organizer and wired it into config.",
  "keywords": ["organizer", "llama.cpp", "config"],
  "importance": 0.7,
  "candidate_memories": [
    {
      "kind": "preference",
      "scope": "project",
      "suggested_tier": "M4",
      "confidence": 0.9,
      "canonical_text": "The project keeps the memory organizer fully local with no remote fallback.",
      "tags": ["memory"],
      "reason": "stated as a hard project rule"
    }
  ]
}`

func TestOrganizeParsesValidResponse(t *testing.T) {
	gen := &fakeGenerator{ready: true, raw: validResponse}
	org := New(gen, Options{})

	result, err := org.Organize(context.Background(), Input{
		SessionID:  "main",
		Transcript: "user: implement the organizer\nassistant: done",
	})
	if err != nil {
		t.Fatalf("Organize() error = %v", err)
	}
	if !strings.Contains(result.Summary, "local organizer") {
		t.Fatalf("summary = %q, want the model summary", result.Summary)
	}
	if len(result.Keywords) != 3 {
		t.Fatalf("keywords = %#v, want 3", result.Keywords)
	}
	if result.Importance != 0.7 {
		t.Fatalf("importance = %v, want 0.7", result.Importance)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want 1", result.Candidates)
	}
	candidate := result.Candidates[0]
	if candidate.Kind != CandidatePreference || candidate.Tier != CandidateTierLongTerm {
		t.Fatalf("candidate typing = %#v", candidate)
	}
	if candidate.Scope != "project" {
		t.Fatalf("candidate scope = %q, want project", candidate.Scope)
	}
	if result.Model != "fake" {
		t.Fatalf("model = %q, want fake", result.Model)
	}
}

func TestOrganizePassesGrammarAndBounds(t *testing.T) {
	gen := &fakeGenerator{ready: true, raw: validResponse}
	org := New(gen, Options{MaxOutputTokens: 900, Temperature: 0.2})

	if _, err := org.Organize(context.Background(), Input{Transcript: "user: hi"}); err != nil {
		t.Fatalf("Organize() error = %v", err)
	}
	if gen.requestCount() != 1 {
		t.Fatalf("generate calls = %d, want 1", gen.requestCount())
	}
	req := gen.requests[0]
	if req.Grammar == "" {
		t.Fatal("expected a grammar to be supplied for constrained decoding")
	}
	if req.MaxTokens != 900 {
		t.Fatalf("max tokens = %d, want 900", req.MaxTokens)
	}
	if req.Temperature != 0.2 {
		t.Fatalf("temperature = %v, want 0.2", req.Temperature)
	}
	if !strings.Contains(req.User, "transcript") {
		t.Fatalf("user payload = %q, want JSON with a transcript field", req.User)
	}
}

func TestOrganizeUnavailableWithoutReadyGenerator(t *testing.T) {
	org := New(&fakeGenerator{ready: false}, Options{})
	_, err := org.Organize(context.Background(), Input{Transcript: "user: hi"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Organize() error = %v, want ErrUnavailable", err)
	}
}

func TestOrganizeNilGeneratorIsUnavailable(t *testing.T) {
	org := New(nil, Options{})
	if org.Available() {
		t.Fatal("organizer with a nil generator must not report Available")
	}
	if _, err := org.Organize(context.Background(), Input{Transcript: "x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Organize() error = %v, want ErrUnavailable", err)
	}
}

func TestOrganizeRejectsEmptyTranscript(t *testing.T) {
	gen := &fakeGenerator{ready: true, raw: validResponse}
	org := New(gen, Options{})
	if _, err := org.Organize(context.Background(), Input{Transcript: "   "}); err == nil {
		t.Fatal("expected an error for an empty transcript")
	}
	if gen.requestCount() != 0 {
		t.Fatal("empty transcript must not reach the model")
	}
}

func TestOrganizeGenerationErrorIsNotSwallowed(t *testing.T) {
	gen := &fakeGenerator{ready: true, err: errors.New("boom")}
	org := New(gen, Options{})
	if _, err := org.Organize(context.Background(), Input{Transcript: "user: hi"}); err == nil {
		t.Fatal("expected the generator error to propagate")
	}
}

func TestOrganizeHonorsTimeout(t *testing.T) {
	gen := &fakeGenerator{ready: true, raw: validResponse, delay: 200 * time.Millisecond}
	org := New(gen, Options{TimeoutSec: 1})
	// A one-second budget must expire before the delayed generator returns.
	gen.delay = 2 * time.Second
	start := time.Now()
	if _, err := org.Organize(context.Background(), Input{Transcript: "user: hi"}); err == nil {
		t.Fatal("expected the organizer deadline to fire")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("timeout took %s, want about 1s", elapsed)
	}
}

func TestOrganizeCapsCandidates(t *testing.T) {
	var items []string
	for i := 0; i < 30; i++ {
		items = append(items, `{"kind":"fact","scope":"project","suggested_tier":"M3","confidence":0.8,"canonical_text":"distinct durable fact number `+string(rune('a'+i))+`","tags":[],"reason":"r"}`)
	}
	raw := `{"session_summary":"s","keywords":[],"importance":0.5,"candidate_memories":[` + strings.Join(items, ",") + `]}`
	gen := &fakeGenerator{ready: true, raw: raw}
	org := New(gen, Options{MaxCandidates: 5})

	result, err := org.Organize(context.Background(), Input{Transcript: "user: hi"})
	if err != nil {
		t.Fatalf("Organize() error = %v", err)
	}
	if len(result.Candidates) != 5 {
		t.Fatalf("candidates = %d, want the configured cap of 5", len(result.Candidates))
	}
}

func TestParseResultAcceptsFencedJSON(t *testing.T) {
	fenced := "Here is the result:\n```json\n" + validResponse + "\n```\nDone."
	result, err := ParseResult(fenced)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want 1", result.Candidates)
	}
}

func TestParseResultAcceptsSurroundingProse(t *testing.T) {
	wrapped := "Sure! " + validResponse + " Let me know if you need more."
	result, err := ParseResult(wrapped)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Summary == "" {
		t.Fatal("expected a summary")
	}
}

func TestParseResultRejectsEmptyResponse(t *testing.T) {
	if _, err := ParseResult("   "); err == nil {
		t.Fatal("expected an error for an empty response")
	}
}

func TestParseResultRejectsResponseWithoutJSON(t *testing.T) {
	if _, err := ParseResult("I could not summarize this session."); err == nil {
		t.Fatal("expected an error when no JSON object is present")
	}
}

func TestParseResultRejectsMissingSummary(t *testing.T) {
	raw := `{"keywords":["a"],"importance":0.5,"candidate_memories":[]}`
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected an error when the summary is missing")
	}
}

func TestParseResultDropsInvalidAndSensitiveCandidates(t *testing.T) {
	raw := `{
      "session_summary": "did work",
      "keywords": [],
      "importance": 0.5,
      "candidate_memories": [
        {"kind":"fact","scope":"project","suggested_tier":"M3","confidence":0.9,"canonical_text":"A perfectly valid durable fact about the build.","tags":[],"reason":"ok"},
        {"kind":"nonsense","scope":"project","suggested_tier":"M3","confidence":0.9,"canonical_text":"Valid text but an unknown kind.","tags":[],"reason":"bad kind"},
        {"kind":"fact","scope":"project","suggested_tier":"M9","confidence":0.9,"canonical_text":"Valid text but an unknown tier.","tags":[],"reason":"bad tier"},
        {"kind":"fact","scope":"project","suggested_tier":"M3","confidence":0.9,"canonical_text":"x","tags":[],"reason":"too short"},
        {"kind":"fact","scope":"project","suggested_tier":"M3","confidence":0.9,"canonical_text":"The deploy token is sk-abcdefghijklmnopqrstuvwxyz0123456789.","tags":[],"reason":"secret"}
      ]
    }`
	result, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %d, want only the valid one: %#v", len(result.Candidates), result.Candidates)
	}
	if !strings.Contains(result.Candidates[0].Text, "valid durable fact") {
		t.Fatalf("kept the wrong candidate: %#v", result.Candidates[0])
	}
}

func TestParseResultEmptyCandidatesIsSuccess(t *testing.T) {
	raw := `{"session_summary":"routine session with nothing durable","keywords":["routine"],"importance":0.3,"candidate_memories":[]}`
	result, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("candidates = %#v, want none", result.Candidates)
	}
}

func TestParseResultDefaultsOmittedCandidateFields(t *testing.T) {
	raw := `{
      "session_summary": "did work",
      "candidate_memories": [
        {"canonical_text":"A durable fact with no declared kind, scope, or tier."}
      ]
    }`
	result, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want 1", result.Candidates)
	}
	candidate := result.Candidates[0]
	if candidate.Kind != CandidateFact {
		t.Fatalf("kind = %q, want fact default", candidate.Kind)
	}
	if candidate.Scope != "project" {
		t.Fatalf("scope = %q, want project default", candidate.Scope)
	}
	if candidate.Tier != CandidateTierShortTerm {
		t.Fatalf("tier = %q, want M3 default", candidate.Tier)
	}
	if candidate.Confidence != 0.7 {
		t.Fatalf("confidence = %v, want 0.7 default", candidate.Confidence)
	}
}

func TestParseResultStripsRolePrefixesFromSummary(t *testing.T) {
	raw := `{"session_summary":"user: fix the build\nassistant: fixed the build\n[tool use: Edit]\nVerified with go test.","candidate_memories":[]}`
	result, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if strings.Contains(strings.ToLower(result.Summary), "user:") {
		t.Fatalf("summary kept a role prefix: %q", result.Summary)
	}
	if strings.Contains(result.Summary, "[tool use") {
		t.Fatalf("summary kept a tool-use echo: %q", result.Summary)
	}
	if !strings.Contains(result.Summary, "Verified with go test.") {
		t.Fatalf("summary dropped real content: %q", result.Summary)
	}
}

func TestParseResultClampsConfidence(t *testing.T) {
	raw := `{"session_summary":"s","candidate_memories":[
      {"kind":"fact","scope":"project","suggested_tier":"M3","confidence":5,"canonical_text":"A durable fact with an out of range confidence value.","tags":[]}
    ]}`
	result, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Candidates[0].Confidence != 1 {
		t.Fatalf("confidence = %v, want clamped to 1", result.Candidates[0].Confidence)
	}
}

func TestOrganizeTruncatesHugeTranscript(t *testing.T) {
	gen := &fakeGenerator{ready: true, raw: validResponse}
	org := New(gen, Options{})
	huge := strings.Repeat("transcript line\n", maxTranscriptRunes)

	if _, err := org.Organize(context.Background(), Input{Transcript: huge}); err != nil {
		t.Fatalf("Organize() error = %v", err)
	}
	if len(gen.requests) != 1 {
		t.Fatalf("generate calls = %d, want 1", len(gen.requests))
	}
	if len([]rune(gen.requests[0].User)) > maxTranscriptRunes+2048 {
		t.Fatalf("payload was not truncated: %d runes", len([]rune(gen.requests[0].User)))
	}
}

func TestOrganizeIncludesPreviousSummary(t *testing.T) {
	gen := &fakeGenerator{ready: true, raw: validResponse}
	org := New(gen, Options{})
	if _, err := org.Organize(context.Background(), Input{
		Transcript:      "user: continue",
		PreviousSummary: "earlier decisions about the schema",
	}); err != nil {
		t.Fatalf("Organize() error = %v", err)
	}
	if !strings.Contains(gen.requests[0].User, "earlier decisions about the schema") {
		t.Fatalf("previous summary missing from payload: %q", gen.requests[0].User)
	}
}

func TestOrganizerClosePropagates(t *testing.T) {
	gen := &fakeGenerator{ready: true, raw: validResponse}
	org := New(gen, Options{})
	if err := org.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	gen.mu.Lock()
	defer gen.mu.Unlock()
	if !gen.closed {
		t.Fatal("expected Close to reach the generator")
	}
}

func TestGrammarIsPresentAndNotEmpty(t *testing.T) {
	if OrganizeGrammar() == "" {
		t.Fatal("expected a non-empty GBNF grammar")
	}
	if !GrammarAvailable() {
		t.Fatal("expected GrammarAvailable to report true")
	}
	if !strings.Contains(OrganizeGrammar(), "root ::=") {
		t.Fatal("grammar must declare a root rule")
	}
}
