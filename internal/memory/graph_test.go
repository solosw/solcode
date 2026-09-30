package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDeriveTopicKeyStable(t *testing.T) {
	a := Item{Kind: KindPreference, Scope: ScopeGlobal, Text: "User prefers table-driven tests in Go packages"}
	b := Item{Kind: KindPreference, Scope: ScopeGlobal, Text: "User prefers table driven tests inside Go packages"}
	ka, kb := DeriveTopicKey(a), DeriveTopicKey(b)
	if ka == "" || kb == "" {
		t.Fatalf("empty topics: %q %q", ka, kb)
	}
	// Same leading content tokens should share a topic prefix after kind.
	if !strings.HasPrefix(ka, "preference:") || !strings.HasPrefix(kb, "preference:") {
		t.Fatalf("topics = %q %q", ka, kb)
	}
}

func TestSelectCurrentBeliefsOnePerTopic(t *testing.T) {
	old := Item{
		ID: "old", Kind: KindConstraint, Scope: ScopeProject, Status: StatusSuperseded,
		Topic: "constraint:plan-mode", Text: "Always enable plan mode for large refactors", Version: 1,
	}
	neu := Item{
		ID: "new", Kind: KindConstraint, Scope: ScopeProject, Status: StatusActive,
		Topic: "constraint:plan-mode", Text: "Never enable plan mode for large refactors", Version: 2,
		Supersedes: "old",
	}
	other := Item{
		ID: "other", Kind: KindPreference, Scope: ScopeGlobal, Status: StatusActive,
		Topic: "preference:dark-theme", Text: "Prefer dark theme in the TUI", Version: 1,
	}
	beliefs := SelectCurrentBeliefs([]Item{old, neu, other}, 10)
	if len(beliefs) != 2 {
		t.Fatalf("beliefs = %#v", beliefs)
	}
	byTopic := map[string]TopicBelief{}
	for _, b := range beliefs {
		byTopic[b.Topic] = b
	}
	if byTopic["constraint:plan-mode"].Item.ID != "new" {
		t.Fatalf("plan-mode winner = %#v", byTopic["constraint:plan-mode"])
	}
	if byTopic["preference:dark-theme"].Item.ID != "other" {
		t.Fatalf("theme winner = %#v", byTopic["preference:dark-theme"])
	}
	text := FormatCurrentBeliefs(beliefs)
	if !strings.Contains(text, "Never enable plan mode") || !strings.Contains(text, "dark theme") {
		t.Fatalf("format = %q", text)
	}
}

func TestTopicHistoryAndGraphEdges(t *testing.T) {
	now := time.Now()
	// Force a shared topic key so the chain is explicit even when lexical
	// overlap is high (dark vs light theme rewrites).
	old := Item{
		ID: "old", Kind: KindPreference, Scope: ScopeGlobal, Status: StatusActive,
		Topic: "preference:tui-theme", Text: "Prefer dark theme in the TUI", Version: 1,
		UpdatedAt: now.Add(-time.Hour),
	}
	neu := Item{
		ID: "new", Kind: KindPreference, Scope: ScopeGlobal, Status: StatusActive,
		Topic: "preference:tui-theme", Text: "Prefer light theme in the TUI instead", Version: 1,
		Supersedes: "old",
	}
	neu, updates := applyGovernanceOnWrite([]Item{old}, neu, now)
	if len(updates) != 1 {
		t.Fatalf("updates = %#v (explicit supersedes id should win)", updates)
	}
	old = updates[0]
	if old.Status != StatusSuperseded {
		t.Fatalf("old status = %s", old.Status)
	}
	items := []Item{old, neu}
	hist := TopicHistoryOf(items, "preference:tui-theme")
	if hist.Active == nil || hist.Active.ID != "new" {
		t.Fatalf("active = %#v", hist.Active)
	}
	if len(hist.Items) < 2 {
		t.Fatalf("history items = %#v", hist.Items)
	}
	g := BuildMemoryGraph(items, 8)
	if len(g.Topics) != 1 {
		t.Fatalf("graph topics = %#v", g.Topics)
	}
	foundSupersede := false
	for _, e := range g.Edges {
		if e.Type == RelSupersedes && e.FromID == "new" && e.ToID == "old" {
			foundSupersede = true
		}
	}
	if !foundSupersede {
		t.Fatalf("missing supersedes edge, edges=%#v relations=%#v supersedes=%q", g.Edges, neu.Relations, neu.Supersedes)
	}
}

func TestExpandGovernanceSetFindsFarPeer(t *testing.T) {
	all := make([]Item, 0, 20)
	for i := 0; i < 15; i++ {
		all = append(all, NewItem("unrelated filler fact number "+string(rune('a'+i))+" about widgets only", TierLongTerm, "s"))
	}
	peer := Item{
		ID: "peer", Kind: KindConstraint, Scope: ScopeProject, Status: StatusActive,
		Topic: "constraint:memory-tests", Text: "Always run go test ./internal/memory before shipping",
	}
	all = append(all, peer)

	cand := Item{
		ID: "cand", Kind: KindConstraint, Scope: ScopeProject, Status: StatusActive,
		Topic: "constraint:memory-tests", Text: "Only run targeted memory test filters on Windows",
	}

	working := all[:10] // peer not included
	gov := expandGovernanceSet(all, working, cand)
	found := false
	for _, item := range gov {
		if item.ID == peer.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("peer not in governance set (set=%d)", len(gov))
	}
}

func TestManagerCurrentBeliefsAndGraph(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	mgr := NewManager(store, nil, nil)

	a := NewItem("Prefer concise replies in Chinese for this project", TierLongTerm, "s1")
	a.Kind = KindPreference
	a.Scope = ScopeProject
	if _, err := store.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := NewItem("Prefer verbose English replies for this project", TierLongTerm, "s1")
	b.Kind = KindPreference
	b.Scope = ScopeProject
	out, err := mgr.RememberOrganizerCandidate(ctx, OrganizerCandidateInput{
		Text: b.Text, Kind: KindPreference, Scope: ScopeProject, Tier: TierLongTerm,
		Confidence: 0.9, Status: "active", Supersedes: a.ID, SourceSessionID: "s1", Model: "t",
	})
	if err != nil || !out.Stored {
		t.Fatalf("store: %#v err=%v", out, err)
	}

	beliefs, err := mgr.CurrentBeliefs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(beliefs) == 0 {
		t.Fatal("expected beliefs")
	}
	found := false
	for _, belief := range beliefs {
		if strings.Contains(belief.Item.Text, "verbose English") {
			found = true
		}
		if strings.Contains(belief.Item.Text, "concise replies") {
			t.Fatalf("superseded belief still current: %#v", belief)
		}
	}
	if !found {
		t.Fatalf("beliefs = %#v", beliefs)
	}
	g, err := mgr.Graph(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if g.Scanned < 2 {
		t.Fatalf("graph = %#v", g)
	}
}
