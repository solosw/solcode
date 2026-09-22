package systemone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeServer stands in for the TypeSafe evaluation endpoint and records the
// last request it received.
type fakeServer struct {
	*httptest.Server
	lastRequest map[string]any
	status      int
	body        string
}

func newFakeServer(t *testing.T, response string) *fakeServer {
	t.Helper()
	fake := &fakeServer{status: http.StatusOK, body: response}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		_ = json.NewDecoder(r.Body).Decode(&decoded)
		fake.lastRequest = decoded
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fake.status)
		_, _ = w.Write([]byte(fake.body))
	}))
	t.Cleanup(fake.Close)
	return fake
}

func testClient(t *testing.T, fake *fakeServer) *Client {
	t.Helper()
	return NewClient(Options{
		BaseURL:    fake.URL,
		APIKey:     "test-key",
		Model:      "jev-test",
		TimeoutSec: 5,
		HTTPClient: fake.Client(),
	})
}

const choiceResponse = `{
  "model": "jev-1.13.0",
  "answers": {
    "department": {"type": "choice", "choice": "returns", "confidence": 0.9,
      "probabilities": {"returns": 0.9, "billing": 0.1}}
  },
  "usage": {"input_tokens": 100, "output_tokens": 10}
}`

func TestAskSendsTypedQuestionsAndDecodesAnswers(t *testing.T) {
	fake := newFakeServer(t, choiceResponse)
	client := testClient(t, fake)

	answers, usage, err := client.Ask(context.Background(), "wrong size shoes", map[string]Question{
		"department": Choice("Which team?", map[string]string{
			"returns": "exchanges and wrong items",
			"billing": "charges and invoices",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 10 {
		t.Fatalf("usage = %+v", usage)
	}
	answer, ok := answers["department"]
	if !ok {
		t.Fatalf("missing department answer: %#v", answers)
	}
	if answer.Choice != "returns" || answer.Confidence != 0.9 {
		t.Fatalf("answer = %+v", answer)
	}
	if answer.Probabilities["billing"] != 0.1 {
		t.Fatalf("probabilities = %#v", answer.Probabilities)
	}

	// The request must carry the documented top-level shape.
	if got := fake.lastRequest["model"]; got != "jev-test" {
		t.Fatalf("model = %v", got)
	}
	questions, ok := fake.lastRequest["questions"].(map[string]any)
	if !ok {
		t.Fatalf("questions = %#v", fake.lastRequest["questions"])
	}
	department, ok := questions["department"].(map[string]any)
	if !ok {
		t.Fatalf("department question = %#v", questions["department"])
	}
	if department["type"] != "choice" {
		t.Fatalf("type = %v", department["type"])
	}
	criteria, ok := department["criteria"].(map[string]any)
	if !ok || len(criteria) != 2 {
		t.Fatalf("criteria = %#v", department["criteria"])
	}
}

func TestAskDecodesNoulAndScore(t *testing.T) {
	response := `{
	  "answers": {
	    "safe": {"type": "noul", "noul": 0.97},
	    "tier": {"type": "score", "score": 2.4, "confidence": 0.8,
	      "legend": ["M1", "M2", "M3", "M4", "M5"]}
	  },
	  "usage": {"input_tokens": 10, "output_tokens": 2}
	}`
	fake := newFakeServer(t, response)
	client := testClient(t, fake)

	answers, _, err := client.Ask(context.Background(), "state", map[string]Question{
		"safe": Noul("Is it safe?"),
		"tier": Score("How durable?", []string{"M1", "M2", "M3", "M4", "M5"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if answers["safe"].Noul != 0.97 {
		t.Fatalf("noul = %v", answers["safe"].Noul)
	}
	if answers["tier"].Score != 2.4 || len(answers["tier"].Legend) != 5 {
		t.Fatalf("score = %+v", answers["tier"])
	}
}

// A response carrying both an object-shaped and an array-shaped probabilities
// field must not fail the whole batch: one malformed answer should not discard
// the others.
func TestAskToleratesArrayShapedProbabilities(t *testing.T) {
	response := `{
	  "answers": {
	    "choice": {"type": "choice", "choice": "a", "confidence": 1,
	      "probabilities": {"a": 1, "b": 0}},
	    "score": {"type": "score", "score": 1, "confidence": 0.5,
	      "probabilities": [0.1, 0.8, 0.1]}
	  }
	}`
	fake := newFakeServer(t, response)
	client := testClient(t, fake)

	answers, _, err := client.Ask(context.Background(), "state", map[string]Question{
		"choice": Choice("pick", map[string]string{"a": "x", "b": "y"}),
		"score":  Score("rate", []string{"low", "mid", "high"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if answers["choice"].Choice != "a" {
		t.Fatalf("choice = %+v", answers["choice"])
	}
	// The score's array distribution is dropped, but the score itself survives.
	if answers["score"].Score != 1 {
		t.Fatalf("score = %+v", answers["score"])
	}
	if answers["score"].Probabilities != nil {
		t.Fatalf("expected sparse probabilities to be dropped, got %#v", answers["score"].Probabilities)
	}
}

func TestAskErrorsOnHTTPFailureAndBusinessError(t *testing.T) {
	fake := newFakeServer(t, `{"error": {"message": "rate limited", "type": "rate_limit"}}`)
	fake.status = http.StatusTooManyRequests
	client := testClient(t, fake)

	_, _, err := client.Ask(context.Background(), "state", map[string]Question{
		"q": Noul("yes?"),
	})
	if err == nil {
		t.Fatal("expected an error for HTTP 429")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
}

func TestAskRejectsUnconfiguredAndInvalidQuestions(t *testing.T) {
	unconfigured := NewClient(Options{BaseURL: "https://example.invalid"})
	if _, _, err := unconfigured.Ask(context.Background(), "state", map[string]Question{"q": Noul("yes?")}); err == nil {
		t.Fatal("expected an error without an api key")
	}

	fake := newFakeServer(t, `{"answers": {}}`)
	client := testClient(t, fake)

	// A Choice with no criteria cannot be answered and must not be sent.
	if _, _, err := client.Ask(context.Background(), "state", map[string]Question{
		"q": {Type: TypeChoice, Instructions: "pick one"},
	}); err == nil {
		t.Fatal("expected an error for a Choice without criteria")
	}
	// An unsupported type is a programming error, not something to send.
	if _, _, err := client.Ask(context.Background(), "state", map[string]Question{
		"q": {Type: "rubric", Instructions: "rate"},
	}); err == nil {
		t.Fatal("expected an error for an unsupported question type")
	}
	// Empty instructions leave the model guessing.
	if _, _, err := client.Ask(context.Background(), "state", map[string]Question{
		"q": Noul("   "),
	}); err == nil {
		t.Fatal("expected an error for empty instructions")
	}
}

func TestAskCachesIdenticalRequests(t *testing.T) {
	var calls int
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(choiceResponse))
	}))
	defer counting.Close()

	client := NewClient(Options{
		BaseURL:    counting.URL,
		APIKey:     "test-key",
		HTTPClient: counting.Client(),
	})
	questions := map[string]Question{
		"department": Choice("Which team?", map[string]string{"returns": "r", "billing": "b"}),
	}
	for i := 0; i < 3; i++ {
		if _, _, err := client.Ask(context.Background(), "same state", questions); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (identical requests should be cached)", calls)
	}
	// Disabling the cache must send every request through.
	uncached := NewClient(Options{
		BaseURL:      counting.URL,
		APIKey:       "test-key",
		HTTPClient:   counting.Client(),
		DisableCache: true,
	})
	for i := 0; i < 2; i++ {
		if _, _, err := uncached.Ask(context.Background(), "same state", questions); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 after two uncached requests", calls)
	}
}

func TestRankOrdersByProbabilityAndSkipsNone(t *testing.T) {
	response := `{
	  "answers": {
	    "ranking": {"type": "choice", "choice": "skill-b", "confidence": 0.8,
	      "probabilities": {"skill-a": 0.2, "skill-b": 0.75, "none": 0.05}}
	  }
	}`
	fake := newFakeServer(t, response)
	client := testClient(t, fake)

	ranked, err := client.Rank(context.Background(), "do the thing",
		"Which skill?", []Candidate{
			{Name: "skill-a", Description: "first"},
			{Name: "skill-b", Description: "second"},
		}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 2 {
		t.Fatalf("ranked = %#v", ranked)
	}
	if ranked[0].Name != "skill-b" || ranked[1].Name != "skill-a" {
		t.Fatalf("order = %v, %v", ranked[0].Name, ranked[1].Name)
	}
	if ranked[0].Probability != 0.75 {
		t.Fatalf("probability = %v", ranked[0].Probability)
	}

	top, err := client.Rank(context.Background(), "do the thing",
		"Which skill?", []Candidate{
			{Name: "skill-a", Description: "first"},
			{Name: "skill-b", Description: "second"},
		}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || top[0].Name != "skill-b" {
		t.Fatalf("topN = %#v", top)
	}
}

func TestRankReturnsNothingWithoutCandidates(t *testing.T) {
	fake := newFakeServer(t, `{"answers": {}}`)
	client := testClient(t, fake)
	ranked, err := client.Rank(context.Background(), "state", "instructions", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 0 {
		t.Fatalf("ranked = %#v", ranked)
	}
	if fake.lastRequest != nil {
		t.Fatal("no request should be sent without candidates")
	}
}
