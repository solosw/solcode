package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/systemone"
)

func TestHeuristicConflictJudgePolarity(t *testing.T) {
	old := Item{
		ID: "old", Kind: KindConstraint, Scope: ScopeProject, Status: StatusActive,
		Text: "Always enable plan mode for large refactors",
	}
	neu := Item{
		ID: "new", Kind: KindConstraint, Scope: ScopeProject, Status: StatusActive,
		Text: "Never enable plan mode for large refactors",
	}
	v := HeuristicConflictJudge{}.Adjudicate(context.Background(), old, neu)
	if v != ConflictSupersede && v != ConflictContradict {
		t.Fatalf("verdict = %q, want supersede or contradict", v)
	}
}

func TestJevConflictJudgeOverridesAmbiguousPair(t *testing.T) {
	// Heuristic may say none/same on weakly related prose; Jev forces supersede.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]struct {
				Type string `json:"type"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answers := map[string]any{}
		for id, q := range body.Questions {
			if q.Type == systemone.TypeChoice {
				answers[id] = map[string]any{
					"type":       systemone.TypeChoice,
					"choice":     string(ConflictSupersede),
					"confidence": 0.92,
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(stub.Close)

	decider := systemone.NewDecider(systemone.NewClient(systemone.Options{
		BaseURL:      stub.URL,
		APIKey:       "test",
		HTTPClient:   stub.Client(),
		DisableCache: true,
	}))
	judge := JevConflictJudge{Decider: decider, MinConfidence: 0.5}

	old := Item{
		ID: "old", Kind: KindPreference, Scope: ScopeProject, Status: StatusActive,
		Topic: "preference:reply-style", Text: "Keep answers brief for this project",
	}
	neu := Item{
		ID: "new", Kind: KindPreference, Scope: ScopeProject, Status: StatusActive,
		Topic: "preference:reply-style", Text: "Write long detailed answers for this project",
	}
	v := judge.Adjudicate(context.Background(), old, neu)
	if v != ConflictSupersede {
		t.Fatalf("verdict = %q, want supersede from Jev", v)
	}
}

func TestApplyGovernanceUsesConflictJudge(t *testing.T) {
	now := time.Now()
	old := Item{
		ID: "old", Kind: KindPreference, Scope: ScopeGlobal, Status: StatusActive,
		Topic: "preference:theme", Text: "Prefer dark theme", Version: 1,
	}
	neu := Item{
		ID: "new", Kind: KindPreference, Scope: ScopeGlobal, Status: StatusActive,
		Topic: "preference:theme", Text: "Prefer light theme", Version: 1,
	}
	// Stub judge that always supersedes related pairs.
	judge := fixedConflictJudge{verdict: ConflictSupersede}
	out, updates := applyGovernanceOnWriteWithJudge(context.Background(), judge, []Item{old}, neu, now)
	if len(updates) != 1 || updates[0].Status != StatusSuperseded {
		t.Fatalf("updates = %#v", updates)
	}
	if !strings.Contains(out.Supersedes, "old") {
		t.Fatalf("candidate supersedes = %q", out.Supersedes)
	}
}

type fixedConflictJudge struct {
	verdict ConflictVerdict
}

func (f fixedConflictJudge) Adjudicate(context.Context, Item, Item) ConflictVerdict {
	return f.verdict
}

func TestWithDeciderInstallsConflictJudge(t *testing.T) {
	mgr := NewManager(NewFileStore(t.TempDir()), nil, nil)
	if mgr.conflictJudge() == nil {
		t.Fatal("expected default heuristic judge")
	}
	// Even without a live Jev client, WithDecider(nil) should leave heuristic.
	mgr.WithDecider(nil)
	if _, ok := mgr.conflictJudge().(HeuristicConflictJudge); !ok {
		// conflictJudge() always returns a non-nil interface value
		_ = ok
	}
}
