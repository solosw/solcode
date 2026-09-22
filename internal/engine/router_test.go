package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/solosw/solcode/internal/systemone"
	"github.com/solosw/solcode/internal/tool"
)

// routerStub answers a ranking Choice with a fixed distribution.
//
// The same distribution is returned for every question id in the batch, which
// is fine here because skill routing sends exactly one question.
type routerStub struct {
	choice        string
	confidence    float64
	probabilities map[string]float64
	requests      int
}

func (s *routerStub) router(t *testing.T) *Router {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests++
		var body struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answers := map[string]any{}
		for id := range body.Questions {
			answers[id] = map[string]any{
				"type":          systemone.TypeChoice,
				"choice":        s.choice,
				"confidence":    s.confidence,
				"probabilities": s.probabilities,
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

func TestRouterRouteSkillsSelectsBest(t *testing.T) {
	stub := &routerStub{
		choice:        "review",
		confidence:    0.85,
		probabilities: map[string]float64{"review": 0.85, "none": 0.1, "deploy": 0.05},
	}
	router := stub.router(t)

	got := router.RouteSkills(context.Background(), "critique this pull request", []SkillInfo{
		{Name: "review", Description: "Review code changes"},
		{Name: "deploy", Description: "Deploy the service"},
	})
	if got != "review" {
		t.Fatalf("got %q, want review", got)
	}
}

func TestRouterRouteSkillsDeclinesOnNone(t *testing.T) {
	stub := &routerStub{
		choice:        "none",
		confidence:    0.9,
		probabilities: map[string]float64{"none": 0.9, "review": 0.1},
	}
	router := stub.router(t)

	got := router.RouteSkills(context.Background(), "unrelated", []SkillInfo{{Name: "review", Description: "Review code"}})
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// routerMisses gates the extra request: it must stay quiet whenever lexical
// matching already found something.
func TestRouterMissesOnlyOnLexicalMiss(t *testing.T) {
	all := []tool.Tool{
		&stubTool{name: "ImageGenerate", desc: "generate images from a prompt"},
		&stubTool{name: "ImageEdit", desc: "edit an existing image"},
	}

	// Nothing enabled and nothing matched: the router should engage.
	misses := routerMisses(all, "make a picture of a cat", map[string]bool{}, nil)
	if len(misses) == 0 {
		t.Fatal("expected the router to engage on a lexical miss")
	}

	// A non-core tool is already enabled: no need to ask.
	if misses := routerMisses(all, "generate images from a prompt", map[string]bool{"ImageGenerate": true}, nil); misses != nil {
		t.Fatalf("got %#v, want nil when a non-core tool is already enabled", misses)
	}

	// Lexical matching hit: no need to ask.
	matched := SelectToolsForTurn(all, nil, "edit an existing image", nil)
	if misses := routerMisses(all, "edit an existing image", map[string]bool{}, matched); misses != nil {
		t.Fatalf("got %#v, want nil after a lexical hit", misses)
	}

	// An empty query has nothing to route on.
	if misses := routerMisses(all, "   ", map[string]bool{}, nil); misses != nil {
		t.Fatalf("got %#v, want nil for an empty query", misses)
	}
}

// routeCandidates must exclude core and hidden tools, since the model already
// has them and enabling them again changes nothing.
func TestRouteCandidatesExcludesCoreAndHidden(t *testing.T) {
	all := []tool.Tool{
		&stubTool{name: tool.ViewToolName, desc: "view a file"},
		&stubTool{name: tool.WaitToolName, desc: "wait"},
		&stubTool{name: "ImageGenerate", desc: "generate images"},
	}
	got := routeCandidates(all, map[string]bool{})
	if len(got) != 1 || got[0].Name() != "ImageGenerate" {
		names := make([]string, 0, len(got))
		for _, candidate := range got {
			names = append(names, candidate.Name())
		}
		t.Fatalf("got %v, want only the non-core non-hidden tool", names)
	}
}

// routedSkills narrows the advertised catalog to the skill Jev picked, which is
// the whole point of routing: the model gets one unambiguous option.
func TestRoutedSkillsNarrowsToChosenSkill(t *testing.T) {
	stub := &routerStub{
		choice:        "verify",
		confidence:    0.9,
		probabilities: map[string]float64{"verify": 0.9, "explore": 0.05, "none": 0.05},
	}
	eng := &Engine{config: Config{
		Router: stub.router(t),
		Skills: []SkillInfo{
			{Name: "explore", Description: "read-only reconnaissance"},
			{Name: "verify", Description: "run the real build and tests"},
		},
	}}
	got := eng.routedSkills(context.Background(), "check that my change works")
	if len(got) != 1 || got[0].Name != "verify" {
		t.Fatalf("got %#v, want only verify", got)
	}
}

// Every path that cannot decide must fall back to the full catalog, so the
// model still selects on its own.
func TestRoutedSkillsFallsBackToFullCatalog(t *testing.T) {
	skills := []SkillInfo{
		{Name: "explore", Description: "recon"},
		{Name: "verify", Description: "validate"},
	}

	// No router configured at all.
	plain := &Engine{config: Config{Skills: skills}}
	if got := plain.routedSkills(context.Background(), "anything"); len(got) != 2 {
		t.Fatalf("got %#v, want the full catalog", got)
	}

	// Router present but no skills to choose from.
	emptyStub := &routerStub{}
	noSkills := &Engine{config: Config{Router: emptyStub.router(t)}}
	if got := noSkills.routedSkills(context.Background(), "anything"); len(got) != 0 {
		t.Fatalf("got %#v, want empty", got)
	}

	// Jev declines: the model keeps the whole catalog.
	declining := &routerStub{
		choice:        "none",
		confidence:    0.95,
		probabilities: map[string]float64{"none": 0.95, "verify": 0.05},
	}
	eng := &Engine{config: Config{Router: declining.router(t), Skills: skills}}
	if got := eng.routedSkills(context.Background(), "unrelated request"); len(got) != 2 {
		t.Fatalf("got %#v, want the full catalog when Jev declines", got)
	}

	// A name Jev returned that we never offered is not usable.
	unknown := &routerStub{
		choice:        "invented-skill",
		confidence:    0.99,
		probabilities: map[string]float64{"invented-skill": 0.99},
	}
	eng = &Engine{config: Config{Router: unknown.router(t), Skills: skills}}
	if got := eng.routedSkills(context.Background(), "anything"); len(got) != 2 {
		t.Fatalf("got %#v, want the full catalog for an unknown name", got)
	}
}
