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

const validResponse = `<result>
  <session_summary>Implemented the local organizer and wired it into config.</session_summary>
  <keywords>
    <k>organizer</k>
    <k>llama.cpp</k>
    <k>config</k>
  </keywords>
  <importance>0.7</importance>
  <candidate_memories>
    <candidate>
      <kind>preference</kind>
      <scope>project</scope>
      <suggested_tier>M4</suggested_tier>
      <confidence>0.9</confidence>
      <canonical_text>The project keeps the memory organizer fully local with no remote fallback.</canonical_text>
      <tags>
        <t>memory</t>
      </tags>
      <reason>stated as a hard project rule</reason>
      <status>active</status>
      <supersedes></supersedes>
    </candidate>
  </candidate_memories>
</result>`

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
	if !strings.Contains(req.Grammar, "<result>") {
		t.Fatalf("grammar = %q, want XML result root", req.Grammar)
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
	if !strings.Contains(req.System, "XML") {
		t.Fatalf("system prompt = %q, want XML instructions", req.System)
	}
}

func TestBuildUserPayloadIncludesSideContext(t *testing.T) {
	payload := buildUserPayload(Input{
		SessionID:       "s1",
		WorkDir:         "/tmp/proj",
		PreviousSummary: "old summary",
		NextSummary:     "new summary",
		Trigger:         "turn",
		ChangedFiles:    []string{"internal/app/organizer_bridge.go"},
		Todos:           []string{"[→] Enrich context"},
		ToolFacts:       []string{"Edited organizer_bridge.go"},
		RelatedMemories: []string{"[preference] mem_x: keep local"},
	}, "user: hi\nassistant: done")
	for _, want := range []string{
		`"trigger":"turn"`,
		"previous_summary",
		"next_summary",
		"changed_files",
		"organizer_bridge.go",
		"todos",
		"tool_facts",
		"related_memories",
		"mem_x",
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("payload missing %q: %s", want, payload)
		}
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
		items = append(items, `<candidate>
      <kind>fact</kind>
      <scope>project</scope>
      <suggested_tier>M3</suggested_tier>
      <confidence>0.8</confidence>
      <canonical_text>distinct durable fact number `+string(rune('a'+i))+`</canonical_text>
      <tags></tags>
      <reason>r</reason>
      <status>active</status>
      <supersedes></supersedes>
    </candidate>`)
	}
	raw := `<result><session_summary>s</session_summary><keywords></keywords><importance>0.5</importance><candidate_memories>` + strings.Join(items, "") + `</candidate_memories></result>`
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

func TestParseResultAcceptsFencedXML(t *testing.T) {
	fenced := "Here is the result:\n```xml\n" + validResponse + "\n```\nDone."
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

func TestParseResultRejectsResponseWithoutXML(t *testing.T) {
	if _, err := ParseResult("I could not summarize this session."); err == nil {
		t.Fatal("expected an error when no XML result is present")
	}
}

func TestParseResultRejectsMissingSummary(t *testing.T) {
	raw := `<result><keywords><k>a</k></keywords><importance>0.5</importance><candidate_memories></candidate_memories></result>`
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected an error when the summary is missing")
	}
}

func TestParseResultDropsInvalidAndSensitiveCandidates(t *testing.T) {
	raw := `<result>
      <session_summary>did work</session_summary>
      <keywords></keywords>
      <importance>0.5</importance>
      <candidate_memories>
        <candidate>
          <kind>fact</kind>
          <scope>project</scope>
          <suggested_tier>M3</suggested_tier>
          <confidence>0.9</confidence>
          <canonical_text>A perfectly valid durable fact about the build.</canonical_text>
          <tags></tags>
          <reason>ok</reason>
        </candidate>
        <candidate>
          <kind>nonsense</kind>
          <scope>project</scope>
          <suggested_tier>M3</suggested_tier>
          <confidence>0.9</confidence>
          <canonical_text>Valid text but an unknown kind.</canonical_text>
          <tags></tags>
          <reason>bad kind</reason>
        </candidate>
        <candidate>
          <kind>fact</kind>
          <scope>project</scope>
          <suggested_tier>M9</suggested_tier>
          <confidence>0.9</confidence>
          <canonical_text>Valid text but an unknown tier.</canonical_text>
          <tags></tags>
          <reason>bad tier</reason>
        </candidate>
        <candidate>
          <kind>fact</kind>
          <scope>project</scope>
          <suggested_tier>M3</suggested_tier>
          <confidence>0.9</confidence>
          <canonical_text>x</canonical_text>
          <tags></tags>
          <reason>too short</reason>
        </candidate>
        <candidate>
          <kind>fact</kind>
          <scope>project</scope>
          <suggested_tier>M3</suggested_tier>
          <confidence>0.9</confidence>
          <canonical_text>The deploy token is sk-abcdefghijklmnopqrstuvwxyz0123456789.</canonical_text>
          <tags></tags>
          <reason>secret</reason>
        </candidate>
      </candidate_memories>
    </result>`
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

func TestParseResultReadsStatusAndSupersedes(t *testing.T) {
	raw := `<result>
  <session_summary>Updated the local organizer governance fields.</session_summary>
  <keywords><k>governance</k></keywords>
  <importance>0.6</importance>
  <candidate_memories>
    <candidate>
      <kind>constraint</kind>
      <scope>project</scope>
      <suggested_tier>M4</suggested_tier>
      <confidence>0.95</confidence>
      <canonical_text>Never load the organizer model inside the solcode process.</canonical_text>
      <tags><t>organizer</t></tags>
      <reason>settled architecture</reason>
      <status>active</status>
      <supersedes>old-inprocess-rule</supersedes>
    </candidate>
  </candidate_memories>
</result>`
	result, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %#v", result.Candidates)
	}
	c := result.Candidates[0]
	if c.Status != "active" {
		t.Fatalf("status = %q", c.Status)
	}
	if c.Supersedes != "old-inprocess-rule" {
		t.Fatalf("supersedes = %q", c.Supersedes)
	}
}

func TestParseResultDefaultsMissingStatusToActive(t *testing.T) {
	raw := `<result>
  <session_summary>Legacy organizer output without governance tags.</session_summary>
  <keywords></keywords>
  <importance>0.4</importance>
  <candidate_memories>
    <candidate>
      <kind>fact</kind>
      <scope>project</scope>
      <suggested_tier>M3</suggested_tier>
      <confidence>0.8</confidence>
      <canonical_text>Legacy candidate without status still parses cleanly.</canonical_text>
      <tags></tags>
      <reason>compat</reason>
    </candidate>
  </candidate_memories>
</result>`
	result, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Status != "active" {
		t.Fatalf("expected default active status, got %#v", result.Candidates)
	}
}

func TestOrganizeGrammarMentionsStatus(t *testing.T) {
	g := OrganizeGrammar()
	for _, want := range []string{"status", "supersedes", "active", "contradicted"} {
		if !strings.Contains(g, want) {
			t.Fatalf("grammar missing %q", want)
		}
	}
}

func TestParseResultDefaultsOmittedCandidateFields(t *testing.T) {
	raw := `<result>
      <session_summary>did work</session_summary>
      <candidate_memories>
        <candidate>
          <canonical_text>A durable fact with no declared kind, scope, or tier.</canonical_text>
        </candidate>
      </candidate_memories>
    </result>`
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
	raw := `<result>
  <session_summary>user: fix the build
assistant: fixed the build
[tool use: Edit]
Verified with go test.</session_summary>
  <candidate_memories></candidate_memories>
</result>`
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
	raw := `<result>
  <session_summary>s</session_summary>
  <candidate_memories>
    <candidate>
      <kind>fact</kind>
      <scope>project</scope>
      <suggested_tier>M3</suggested_tier>
      <confidence>5</confidence>
      <canonical_text>A durable fact with an out of range confidence value.</canonical_text>
      <tags></tags>
    </candidate>
  </candidate_memories>
</result>`
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
	if !strings.Contains(OrganizeGrammar(), "<result>") {
		t.Fatal("grammar must constrain the XML result root")
	}
	if strings.Contains(OrganizeGrammar(), "session_summary\"") {
		t.Fatal("grammar still looks like the old JSON shape")
	}
	// Unbounded free text is what let MiniCPM-1B loop inside <session_summary>
	// without ever emitting a closing tag. The grammar must keep finite bounds.
	if strings.Contains(OrganizeGrammar(), "text ::= char*") {
		t.Fatal("grammar must not use unbounded text ::= char*")
	}
	if !strings.Contains(OrganizeGrammar(), "summary-text") {
		t.Fatal("grammar must declare a bounded summary-text rule")
	}
	if !strings.Contains(OrganizeGrammar(), "char{") {
		t.Fatal("grammar must bound free text with char{m,n} repetitions")
	}
}
