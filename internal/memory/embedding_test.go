package memory

import (
	"context"
	"testing"
	"time"

	"github.com/philippgille/chromem-go"
	"github.com/solosw/solcode/internal/embedding"
	"github.com/solosw/solcode/internal/systemone"
)

type stubVectorIndex struct {
	added []string
	hits  []chromem.Result
}

func (s *stubVectorIndex) Add(ctx context.Context, id, content string, metadata map[string]string, vec []float32) error {
	_ = ctx
	_ = content
	_ = metadata
	_ = vec
	s.added = append(s.added, id)
	return nil
}

func (s *stubVectorIndex) Query(ctx context.Context, text string, n int, where map[string]string) ([]chromem.Result, error) {
	_ = ctx
	_ = text
	_ = n
	_ = where
	return append([]chromem.Result(nil), s.hits...), nil
}

func (s *stubVectorIndex) Provider() embedding.Provider { return nil }

type stubEval struct {
	answers systemone.Answers
}

func (s stubEval) Ask(ctx context.Context, state any, questions map[string]systemone.Question) (systemone.Answers, systemone.Usage, error) {
	_ = ctx
	_ = state
	_ = questions
	return s.answers, systemone.Usage{}, nil
}
func (s stubEval) Configured() bool { return true }
func (s stubEval) Model() string    { return "stub" }
func (s stubEval) Backend() string  { return systemone.BackendAPI }

func TestIndexMemoryOnRememberDirect(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	idx := &stubVectorIndex{}
	mgr := NewManager(store, DefaultGate{}, nil)
	mgr.Vectors = idx

	ctx := context.Background()
	out, err := mgr.RememberDirect(ctx, DirectInput{
		Text:            "prefer table-driven tests",
		Kind:            KindPreference,
		Scope:           ScopeProject,
		Tier:            TierLongTerm,
		Tags:            []string{"testing"},
		SourceSessionID: "s1",
		AllowDuplicate:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Stored {
		t.Fatalf("not stored: %+v", out)
	}
	if len(idx.added) != 1 || idx.added[0] != out.Item.ID {
		t.Fatalf("indexed = %#v, want [%q]", idx.added, out.Item.ID)
	}
}

func TestRetrieveMergesVectorHits(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	ctx := context.Background()
	now := time.Now()
	lexical, err := store.Save(ctx, Item{
		ID: "lex", Tier: TierLongTerm, Kind: KindFact, Scope: ScopeProject,
		Text: "concise lexical match", Importance: 0.8, Confidence: 0.8,
		AccessCount: 1, UpdatedAt: now, LastAccessedAt: now, SourceSessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	vectorOnly, err := store.Save(ctx, Item{
		ID: "vec", Tier: TierLongTerm, Kind: KindPreference, Scope: ScopeProject,
		Text: "unrelated wording about brevity style", Importance: 0.9, Confidence: 0.9,
		AccessCount: 1, UpdatedAt: now, LastAccessedAt: now, SourceSessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = lexical

	idx := &stubVectorIndex{hits: []chromem.Result{{ID: vectorOnly.ID, Similarity: 0.9}}}
	mgr := NewManager(store, DefaultGate{}, nil)
	mgr.Vectors = idx

	got, err := mgr.Retrieve(ctx, "concise", "s1", true, 5)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, item := range got {
		ids[item.ID] = true
	}
	if !ids["lex"] {
		t.Fatalf("missing lexical hit: %#v", got)
	}
	if !ids["vec"] {
		t.Fatalf("missing vector merge hit: %#v", got)
	}
}

func TestRetrieveRanksWithJev(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	ctx := context.Background()
	now := time.Now()
	a, err := store.Save(ctx, Item{
		ID: "a", Tier: TierLongTerm, Kind: KindFact, Scope: ScopeProject,
		Text: "alpha fact about concise style", Importance: 0.8, Confidence: 0.8,
		AccessCount: 1, UpdatedAt: now, LastAccessedAt: now, SourceSessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Save(ctx, Item{
		ID: "b", Tier: TierLongTerm, Kind: KindFact, Scope: ScopeProject,
		Text: "beta fact about concise answers", Importance: 0.8, Confidence: 0.8,
		AccessCount: 1, UpdatedAt: now.Add(-time.Minute), LastAccessedAt: now.Add(-time.Minute), SourceSessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = a
	_ = b

	decider := systemone.NewDecider(stubEval{answers: systemone.Answers{
		"ranking": {
			Type:       systemone.TypeChoice,
			Choice:     "b",
			Confidence: 0.9,
			Probabilities: map[string]float64{
				"b": 0.8,
				"a": 0.2,
			},
		},
	}})
	mgr := NewManager(store, DefaultGate{}, nil).WithDecider(decider)
	got, err := mgr.Retrieve(ctx, "concise", "s1", true, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "b" {
		t.Fatalf("expected Jev to prefer b first, got %#v", got)
	}
}

func TestEmbedContentAndMetadata(t *testing.T) {
	item := Item{
		ID: "x", Tier: TierLongTerm, Kind: KindPreference, Scope: ScopeProject,
		Text: "prefers table-driven tests", Tags: []string{"testing", "style"},
		SourceSessionID: "s1", SourceTurn: 3,
	}
	content := embedContent(item)
	if content != "[preference] prefers table-driven tests #testing #style" {
		t.Fatalf("content = %q", content)
	}
	meta := embedMetadata(item)
	if meta["tier"] != "M4" || meta["kind"] != "preference" || meta["scope"] != "project" {
		t.Fatalf("meta = %#v", meta)
	}
	if meta["source_session_id"] != "s1" || meta["source_turn"] != "3" || meta["tags"] != "testing,style" {
		t.Fatalf("meta = %#v", meta)
	}
}
