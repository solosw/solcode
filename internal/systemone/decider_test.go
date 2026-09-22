package systemone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeciderFallsBackWhenDisabled(t *testing.T) {
	// No client at all: every method must still answer.
	var decider *Decider
	if decider.Enabled() {
		t.Fatal("nil decider should not be enabled")
	}
	if got, confidence := decider.Choose(context.Background(), "s", "q", map[string]string{"a": "x"}, 0.6, "a"); got != "a" || confidence != 0 {
		t.Fatalf("Choose = %q, %v", got, confidence)
	}
	if got, _ := decider.Noul(context.Background(), "s", "q", 0.8, 0.2, true); got != true {
		t.Fatalf("Noul = %v, want the fallback", got)
	}
	if ranked := decider.Rank(context.Background(), "s", "q", []Candidate{{Name: "a"}}, 1); ranked != nil {
		t.Fatalf("Rank = %#v, want nil", ranked)
	}

	// A client without a key is equally unusable.
	unconfigured := NewDecider(NewClient(Options{BaseURL: "https://example.invalid"}))
	if unconfigured.Enabled() {
		t.Fatal("decider without an api key should not be enabled")
	}
	if got, confidence := unconfigured.Choose(context.Background(), "s", "q", map[string]string{"a": "x"}, 0.6, "a"); got != "a" || confidence != 0 {
		t.Fatalf("Choose = %q, %v", got, confidence)
	}
}

// A client whose endpoint fails must degrade to the fallback rather than
// propagating an error or, worse, returning a guessed answer.
func TestDeciderFallsBackWhenCallFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	var reported []error
	decider := NewDecider(NewClient(Options{
		BaseURL:    server.URL,
		APIKey:     "k",
		HTTPClient: server.Client(),
	})).WithErrorHandler(func(err error) { reported = append(reported, err) })
	if !decider.Enabled() {
		t.Fatal("decider should be enabled")
	}

	got, confidence := decider.Choose(context.Background(), "s", "q",
		map[string]string{"a": "x", "b": "y"}, 0.6, "b")
	if got != "b" || confidence != 0 {
		t.Fatalf("Choose = %q, %v want fallback b/0", got, confidence)
	}
	if len(reported) == 0 {
		t.Fatal("expected the failure to be reported")
	}
}

// An option the caller never offered is not a usable answer.
func TestDeciderRejectsUnknownChoice(t *testing.T) {
	fake := newFakeServer(t, `{"answers": {"question": {"type": "choice", "choice": "invented", "confidence": 1}}}`)
	decider := NewDecider(testClient(t, fake))

	got, confidence := decider.Choose(context.Background(), "s", "q",
		map[string]string{"a": "x", "b": "y"}, 0.6, "b")
	if got != "b" || confidence != 0 {
		t.Fatalf("Choose = %q, %v want fallback b/0", got, confidence)
	}
}

func TestDeciderHonorsConfidenceFloor(t *testing.T) {
	fake := newFakeServer(t, `{"answers": {"question": {"type": "choice", "choice": "a", "confidence": 0.4,
	  "probabilities": {"a": 0.4, "b": 0.35, "c": 0.25}}}}`)
	decider := NewDecider(testClient(t, fake))

	// Below the floor: keep the fallback but surface the measured confidence.
	got, confidence := decider.Choose(context.Background(), "s", "q",
		map[string]string{"a": "x", "b": "y", "c": "z"}, 0.6, "b")
	if got != "b" {
		t.Fatalf("Choose = %q, want fallback b", got)
	}
	if confidence != 0.4 {
		t.Fatalf("confidence = %v, want the measured 0.4", confidence)
	}
}

// Noul uses three bands, and the middle band is deliberately uncertain.
func TestDeciderNoulBands(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     bool
	}{
		{"high is yes", `{"answers": {"question": {"type": "noul", "noul": 0.95}}}`, true},
		{"low is no", `{"answers": {"question": {"type": "noul", "noul": 0.05}}}`, false},
		{"middle uses fallback", `{"answers": {"question": {"type": "noul", "noul": 0.5}}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeServer(t, tc.response)
			decider := NewDecider(testClient(t, fake))
			got, probability := decider.Noul(context.Background(), "s", "q", 0.8, 0.2, true)
			if got != tc.want {
				t.Fatalf("Noul = %v, want %v", got, tc.want)
			}
			if probability == 0 {
				t.Fatal("expected a measured probability")
			}
		})
	}
}

// The guardrail must fail closed: an unavailable check is "escalate", never
// "allow". This is the single most important property in the file.
func TestGuardrailFailsClosedWhenUnavailable(t *testing.T) {
	var guardrail *Guardrail
	verdict := guardrail.CheckToolInput(context.Background(), "Bash", "rm -rf /")
	if verdict.Unsafe {
		t.Fatal("a nil guardrail should not invent an unsafe verdict")
	}
	// With no guardrail configured there is no check, so the permission service
	// remains the sole control. That is not "uncertain".
	if verdict.Uncertain {
		t.Fatalf("verdict = %+v", verdict)
	}

	// With a guardrail configured but unreachable, the call must be held.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	broken := &Guardrail{Decider: NewDecider(NewClient(Options{
		BaseURL:    server.URL,
		APIKey:     "k",
		HTTPClient: server.Client(),
	}))}
	verdict = broken.CheckToolInput(context.Background(), "Bash", "rm -rf /")
	if !verdict.Uncertain {
		t.Fatalf("verdict = %+v, want uncertain (fail closed)", verdict)
	}
	if verdict.Allowed() {
		t.Fatal("an unavailable guardrail must not allow the call")
	}
}

func TestGuardrailFlagsHazards(t *testing.T) {
	response := `{
	  "answers": {
	    "secrets": {"type": "noul", "noul": 0.02},
	    "destructive": {"type": "noul", "noul": 0.95},
	    "exfiltration": {"type": "noul", "noul": 0.1},
	    "privilege": {"type": "noul", "noul": 0.1}
	  }
	}`
	fake := newFakeServer(t, response)
	guardrail := &Guardrail{Decider: NewDecider(testClient(t, fake))}

	verdict := guardrail.CheckToolInput(context.Background(), "Bash", "rm -rf /var/data")
	if !verdict.Unsafe {
		t.Fatalf("verdict = %+v, want unsafe", verdict)
	}
	if verdict.Reason != "destructive" {
		t.Fatalf("reason = %q, want the hazard that fired", verdict.Reason)
	}
	if verdict.Allowed() {
		t.Fatal("an unsafe verdict must not be allowed")
	}
}

func TestGuardrailAllowsCleanInput(t *testing.T) {
	response := `{
	  "answers": {
	    "secrets": {"type": "noul", "noul": 0.01},
	    "destructive": {"type": "noul", "noul": 0.02},
	    "exfiltration": {"type": "noul", "noul": 0.01},
	    "privilege": {"type": "noul", "noul": 0.01}
	  }
	}`
	fake := newFakeServer(t, response)
	guardrail := &Guardrail{Decider: NewDecider(testClient(t, fake))}

	verdict := guardrail.CheckToolInput(context.Background(), "Bash", "go test ./...")
	if !verdict.Allowed() {
		t.Fatalf("verdict = %+v, want allowed", verdict)
	}
}

// A hazard probability in the middle band escalates rather than deciding.
func TestGuardrailEscalatesAmbiguousInput(t *testing.T) {
	response := `{
	  "answers": {
	    "secrets": {"type": "noul", "noul": 0.5},
	    "destructive": {"type": "noul", "noul": 0.1},
	    "exfiltration": {"type": "noul", "noul": 0.1},
	    "privilege": {"type": "noul", "noul": 0.1}
	  }
	}`
	fake := newFakeServer(t, response)
	guardrail := &Guardrail{Decider: NewDecider(testClient(t, fake))}

	verdict := guardrail.CheckToolInput(context.Background(), "Write", "some content")
	if !verdict.Uncertain {
		t.Fatalf("verdict = %+v, want uncertain", verdict)
	}
	if verdict.Allowed() {
		t.Fatal("an uncertain verdict must not be allowed through")
	}
}

func TestGuardrailSkipsEmptyPayload(t *testing.T) {
	fake := newFakeServer(t, `{"answers": {}}`)
	guardrail := &Guardrail{Decider: NewDecider(testClient(t, fake))}
	verdict := guardrail.CheckToolInput(context.Background(), "Bash", "   ")
	if verdict.Uncertain || verdict.Unsafe {
		t.Fatalf("verdict = %+v", verdict)
	}
	if fake.lastRequest != nil {
		t.Fatal("no request should be sent for an empty payload")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Fatalf("got %q", got)
	}
	if got := truncateRunes("abcdef", 3); got != "abc…" {
		t.Fatalf("got %q", got)
	}
	// Multi-byte input must be cut on rune boundaries, not bytes.
	if got := truncateRunes("中文测试", 2); got != "中文…" {
		t.Fatalf("got %q", got)
	}
}
