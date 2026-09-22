package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/systemone"
	"github.com/solosw/solcode/internal/tool"
)

// screenStub answers a batch of Noul screening questions with a per-candidate
// probability, keyed by the candidate name it recognizes in the question text.
type screenStub struct {
	// probabilities maps a candidate name to the probability of "yes".
	probabilities map[string]float64
	// requests counts evaluation calls.
	requests int
	// lastQuestionCount records how many questions were asked at once.
	lastQuestionCount int
}

func (s *screenStub) router(t *testing.T) *Router {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests++
		var body struct {
			Questions map[string]struct {
				Type         string `json:"type"`
				Instructions any    `json:"instructions"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.lastQuestionCount = len(body.Questions)

		answers := map[string]any{}
		for id, question := range body.Questions {
			text, _ := question.Instructions.(string)
			answers[id] = map[string]any{
				"type": systemone.TypeNoul,
				"noul": s.probabilityFor(text),
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(server.Close)
	decider := systemone.NewDecider(systemone.NewClient(systemone.Options{
		BaseURL:      server.URL,
		APIKey:       "test-key",
		HTTPClient:   server.Client(),
		DisableCache: true,
	}))
	return &Router{Decider: decider, MinConfidence: 0.6, TopN: 2}
}

// probabilityFor finds the first candidate name mentioned in the question text.
func (s *screenStub) probabilityFor(question string) float64 {
	for name, probability := range s.probabilities {
		if strings.Contains(question, "`"+name+"`") {
			return probability
		}
	}
	return 0
}

func twoTools() []tool.Tool {
	return []tool.Tool{
		&stubTool{name: "ImageGenerate", desc: "generate images"},
		&stubTool{name: "ImageEdit", desc: "edit images"},
	}
}

// Screening judges every candidate in one request and enables all that clear the
// floor — this is the point of a Noul batch over a single Choice, which would
// return only one winner even when several tools are relevant.
func TestRouteToolsScreensAllCandidatesInOneRequest(t *testing.T) {
	stub := &screenStub{probabilities: map[string]float64{
		"ImageGenerate": 0.9,
		"ImageEdit":     0.85,
	}}
	router := stub.router(t)

	got := router.RouteTools(context.Background(), "make an image and then tweak it", twoTools())
	if len(got) != 2 {
		t.Fatalf("got %#v, want both candidates above the floor", got)
	}
	if stub.requests != 1 {
		t.Fatalf("requests = %d, want a single batched call", stub.requests)
	}
	if stub.lastQuestionCount != 2 {
		t.Fatalf("questions = %d, want one per candidate", stub.lastQuestionCount)
	}
}

func TestRouteToolsDropsCandidatesBelowFloor(t *testing.T) {
	stub := &screenStub{probabilities: map[string]float64{
		"ImageGenerate": 0.9,
		"ImageEdit":     0.2,
	}}
	router := stub.router(t)

	got := router.RouteTools(context.Background(), "make a picture of a cat", twoTools())
	if len(got) != 1 || got[0] != "ImageGenerate" {
		t.Fatalf("got %#v, want only ImageGenerate", got)
	}
}

// Nothing above the floor means no opinion, and the caller keeps its lexical
// selection rather than being handed an empty override.
func TestRouteToolsReturnsNothingWhenAllBelowFloor(t *testing.T) {
	stub := &screenStub{probabilities: map[string]float64{
		"ImageGenerate": 0.3,
		"ImageEdit":     0.25,
	}}
	router := stub.router(t)

	if got := router.RouteTools(context.Background(), "do something vague", twoTools()); got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}

// Screening results are ordered best first so the caller can truncate.
func TestRouteToolsOrdersByProbability(t *testing.T) {
	stub := &screenStub{probabilities: map[string]float64{
		"ImageGenerate": 0.7,
		"ImageEdit":     0.95,
	}}
	router := stub.router(t)

	got := router.RouteTools(context.Background(), "edit and generate images", twoTools())
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	if got[0] != "ImageEdit" {
		t.Fatalf("got %#v, want the higher probability first", got)
	}
}

// When there are more candidates than the screening budget, the strongest
// lexical matches must be the ones screened — dropping the most plausible
// options to save budget would defeat the purpose.
func TestRouteToolsScreensStrongestLexicalCandidates(t *testing.T) {
	var tools []tool.Tool
	probabilities := map[string]float64{}
	// One clearly relevant tool plus more than the budget of irrelevant ones.
	tools = append(tools, &stubTool{name: "ImageGenerate", desc: "generate an image from a prompt"})
	probabilities["ImageGenerate"] = 0.99
	for i := 0; i < maxScreenedCandidates+10; i++ {
		name := "filler_" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
		tools = append(tools, &stubTool{name: name, desc: "unrelated filler capability"})
		probabilities[name] = 0.05
	}
	stub := &screenStub{probabilities: probabilities}
	router := stub.router(t)

	got := router.RouteTools(context.Background(), "generate an image", tools)
	if stub.lastQuestionCount != maxScreenedCandidates {
		t.Fatalf("questions = %d, want the budget %d", stub.lastQuestionCount, maxScreenedCandidates)
	}
	if len(got) != 1 || got[0] != "ImageGenerate" {
		t.Fatalf("got %#v, want the lexically strongest candidate to survive screening", got)
	}
}

func TestRouteToolsWithoutDeciderIsNoop(t *testing.T) {
	router := &Router{}
	if got := router.RouteTools(context.Background(), "anything", twoTools()); got != nil {
		t.Fatalf("got %#v", got)
	}
	if got := router.RouteTools(context.Background(), "   ", twoTools()); got != nil {
		t.Fatalf("got %#v", got)
	}
	if got := router.RouteTools(context.Background(), "anything", nil); got != nil {
		t.Fatalf("got %#v", got)
	}
}

// A candidate whose name never appears in the question is not judgeable, so
// scores of zero must not enable it.
func TestRouteToolsIgnoresUnjudgedCandidates(t *testing.T) {
	stub := &screenStub{probabilities: map[string]float64{}}
	router := stub.router(t)
	if got := router.RouteTools(context.Background(), "anything", twoTools()); got != nil {
		t.Fatalf("got %#v, want nil when nothing scores", got)
	}
}

// screenedCandidates must not invent a "none" option: screening judges each
// capability independently, so declining is every probability falling low.
func TestScreenedCandidatesHaveNoNoneOption(t *testing.T) {
	got := screenedCandidates(twoTools())
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	for _, candidate := range got {
		if candidate.Name == routerNoneOption {
			t.Fatal("screening candidates must not include a none sentinel")
		}
	}
}

func TestScreenedCandidatesSkipsHiddenTools(t *testing.T) {
	got := screenedCandidates([]tool.Tool{
		&stubTool{name: tool.WaitToolName, desc: "wait"},
		&stubTool{name: tool.SubagentToolName, desc: "subagent"},
		&stubTool{name: "ImageGenerate", desc: "generate images"},
	})
	if len(got) != 1 || got[0].Name != "ImageGenerate" {
		t.Fatalf("got %#v, want only the visible tool", got)
	}
}

// Skill routing still uses a Choice: exactly one skill should run, so a single
// winner is the correct shape there.
func TestRouteSkillsStillUsesChoice(t *testing.T) {
	stub := &routerStub{
		choice:        "verify",
		confidence:    0.9,
		probabilities: map[string]float64{"verify": 0.9, "none": 0.1},
	}
	router := stub.router(t)
	got := router.RouteSkills(context.Background(), "check that my change works", []SkillInfo{
		{Name: "verify", Description: "validate the change"},
		{Name: "explore", Description: "read-only reconnaissance"},
	})
	if got != "verify" {
		t.Fatalf("got %q, want verify", got)
	}
}
