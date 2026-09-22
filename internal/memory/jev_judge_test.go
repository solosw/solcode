package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/solosw/solcode/internal/systemone"
)

// jevStub answers each question according to its type, so a test can describe
// the decisions it wants without mirroring the wire format.
type jevStub struct {
	// choice is returned for any Choice question.
	choice string
	// confidence accompanies the Choice answer.
	confidence float64
	// score is returned for Score questions.
	score float64
	// noul is returned for Noul questions.
	noul float64
	// omitAll answers with an empty batch, simulating a malformed response.
	omitAll bool
	// requests counts how many evaluation calls were made.
	requests int
}

func (s *jevStub) decider(t *testing.T) *systemone.Decider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests++
		var body struct {
			Questions map[string]struct {
				Type string `json:"type"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		answers := map[string]any{}
		if !s.omitAll {
			for id, question := range body.Questions {
				switch question.Type {
				case systemone.TypeChoice:
					answers[id] = map[string]any{
						"type":       systemone.TypeChoice,
						"choice":     s.choice,
						"confidence": s.confidence,
					}
				case systemone.TypeScore:
					answers[id] = map[string]any{
						"type":       systemone.TypeScore,
						"score":      s.score,
						"confidence": s.confidence,
					}
				case systemone.TypeNoul:
					answers[id] = map[string]any{
						"type": systemone.TypeNoul,
						"noul": s.noul,
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(server.Close)
	return systemone.NewDecider(systemone.NewClient(systemone.Options{
		BaseURL:      server.URL,
		APIKey:       "test-key",
		HTTPClient:   server.Client(),
		DisableCache: true,
	}))
}

func TestJevJudgeClassifiesKindScopeAndTier(t *testing.T) {
	stub := &jevStub{choice: string(KindConstraint), confidence: 0.9, score: 4, noul: 0.95}
	judge := JevJudge{Decider: stub.decider(t), MinConfidence: 0.6}

	judgement, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{
		Text: "Never edit files under internal/generated; the build produces them.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if judgement.Kind != KindConstraint {
		t.Fatalf("kind = %q, want constraint", judgement.Kind)
	}
	// "constraint" is not a valid Scope, so the scope question falls back to the
	// static default instead of accepting an option we never offered.
	if judgement.Scope != ScopeProject {
		t.Fatalf("scope = %q, want the fallback project scope", judgement.Scope)
	}
	// Score 4 rounds to index 4, the last level, which is M5.
	if judgement.SuggestedTier != TierProcedural {
		t.Fatalf("tier = %q, want M5", judgement.SuggestedTier)
	}
	if !judgement.ShouldStore {
		t.Fatal("expected should_store")
	}
	if judgement.CanonicalText == "" {
		t.Fatal("canonical text must be preserved")
	}
}

func TestJevJudgeMapsScoreOntoTiers(t *testing.T) {
	cases := []struct {
		score float64
		want  Tier
	}{
		{0, TierSensory},
		{1, TierWorking},
		{2, TierShortTerm},
		{3, TierLongTerm},
		{4, TierProcedural},
		// A position between levels rounds to the nearer one.
		{2.4, TierShortTerm},
		{2.6, TierLongTerm},
		// Out-of-range positions clamp instead of panicking.
		{99, TierProcedural},
	}
	for _, tc := range cases {
		stub := &jevStub{choice: string(KindFact), confidence: 0.9, score: tc.score}
		judge := JevJudge{Decider: stub.decider(t), MinConfidence: 0.6}
		judgement, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{Text: "a fact"})
		if err != nil {
			t.Fatal(err)
		}
		if judgement.SuggestedTier != tc.want {
			t.Fatalf("score %v -> tier %q, want %q", tc.score, judgement.SuggestedTier, tc.want)
		}
	}
}

func TestJevJudgeUsesScopeChoiceWhenValid(t *testing.T) {
	stub := &jevStub{choice: string(ScopeGlobal), confidence: 0.85, score: 3}
	judge := JevJudge{Decider: stub.decider(t), MinConfidence: 0.6}
	judgement, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{
		Text: "The user always prefers table-driven tests.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if judgement.Scope != ScopeGlobal {
		t.Fatalf("scope = %q, want global", judgement.Scope)
	}
	if judgement.Kind != KindFact {
		// Kind also received ScopeGlobal, which is not a valid Kind, so it falls
		// back rather than storing an unusable category.
		t.Fatalf("kind = %q, want the fallback fact", judgement.Kind)
	}
}

// A low-confidence answer must leave the deterministic fallback in place rather
// than storing a category the model could not justify.
func TestJevJudgeKeepsFallbackOnLowConfidence(t *testing.T) {
	stub := &jevStub{choice: string(KindWorkflow), confidence: 0.2, score: 4}
	judge := JevJudge{Decider: stub.decider(t), MinConfidence: 0.6}

	judgement, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{
		Text: "build with go build ./cmd/solcode",
	})
	if err != nil {
		t.Fatal(err)
	}
	if judgement.Kind != KindFact {
		t.Fatalf("kind = %q, want the static fallback fact", judgement.Kind)
	}
	if judgement.Scope != ScopeProject {
		t.Fatalf("scope = %q, want the static fallback project", judgement.Scope)
	}
	if judgement.SuggestedTier != TierShortTerm {
		t.Fatalf("tier = %q, want the static fallback M3", judgement.SuggestedTier)
	}
}

// With Jev unreachable the judge must return a usable judgement, not an error:
// losing the classification must not lose the memory.
func TestJevJudgeFallsBackWhenUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer server.Close()
	judge := JevJudge{Decider: systemone.NewDecider(systemone.NewClient(systemone.Options{
		BaseURL:    server.URL,
		APIKey:     "k",
		HTTPClient: server.Client(),
	}))}

	judgement, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{Text: "some durable fact"})
	if err != nil {
		t.Fatalf("a degraded decision service must not fail the write: %v", err)
	}
	if !judgement.ShouldStore || judgement.Kind != KindFact {
		t.Fatalf("judgement = %+v, want the static fallback", judgement)
	}
}

func TestJevJudgeEmptyTextDoesNotStore(t *testing.T) {
	stub := &jevStub{}
	judge := JevJudge{Decider: stub.decider(t)}
	judgement, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{Text: "   "})
	if err != nil {
		t.Fatal(err)
	}
	if judgement.ShouldStore {
		t.Fatalf("judgement = %+v, want should_store false", judgement)
	}
	if stub.requests != 0 {
		t.Fatalf("requests = %d, empty text should not call out", stub.requests)
	}
}

// A nil decider is the normal "Jev is off" case and must match StaticJudge.
func TestJevJudgeWithoutDeciderMatchesStaticJudge(t *testing.T) {
	judge := JevJudge{}
	got, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{Text: "a fact"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := StaticJudge{}.JudgeMemory(context.Background(), MemoryJudgementInput{Text: "a fact"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != want.Kind || got.Scope != want.Scope || got.SuggestedTier != want.SuggestedTier {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// A malformed response must not turn into a stored memory with invented fields.
func TestJevJudgeHandlesEmptyResponseBatch(t *testing.T) {
	stub := &jevStub{omitAll: true}
	judge := JevJudge{Decider: stub.decider(t), MinConfidence: 0.6}
	judgement, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{Text: "a fact"})
	if err != nil {
		t.Fatal(err)
	}
	if judgement.Kind != KindFact || judgement.Scope != ScopeProject {
		t.Fatalf("judgement = %+v, want the static fallback", judgement)
	}
}

// A confident "no lasting value" Noul must stop the memory being stored.
func TestJevJudgeStopsTransientMemory(t *testing.T) {
	stub := &jevStub{choice: string(KindFact), confidence: 0.9, score: 1, noul: 0.05}
	judge := JevJudge{Decider: stub.decider(t), MinConfidence: 0.6}
	judgement, err := judge.JudgeMemory(context.Background(), MemoryJudgementInput{Text: "hmm ok"})
	if err != nil {
		t.Fatal(err)
	}
	if judgement.ShouldStore {
		t.Fatalf("judgement = %+v, want should_store false for transient text", judgement)
	}
}

// The extractor keeps the deterministic tool-trace candidates when Jev is off,
// so disabling Jev changes classification but never loses extracted work.
func TestJevExtractorWithoutJevKeepsCandidates(t *testing.T) {
	extractor := JevExtractor{}
	got, err := extractor.ExtractMemories(context.Background(), ExtractionInput{})
	if err != nil {
		t.Fatal(err)
	}
	want := extractToolTraceMemories(ExtractionInput{})
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d", len(got), len(want))
	}
}
