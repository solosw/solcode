package memory

import (
	"context"
	"testing"
	"time"
)

func TestItemIsActiveRespectsStatusAndExpiry(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	active := Item{Status: StatusActive, Text: "ok"}
	if !active.IsActive(now) {
		t.Fatal("active item should be active")
	}
	legacy := Item{Text: "legacy empty status"}
	if !legacy.IsActive(now) {
		t.Fatal("empty status must default to active")
	}
	superseded := Item{Status: StatusSuperseded, Text: "old"}
	if superseded.IsActive(now) {
		t.Fatal("superseded must be inactive")
	}
	expired := Item{Status: StatusActive, ExpiresAt: now.Add(-time.Minute), Text: "gone"}
	if expired.IsActive(now) {
		t.Fatal("past ExpiresAt must be inactive")
	}
	future := Item{Status: StatusActive, ExpiresAt: now.Add(time.Hour), Text: "soon"}
	if !future.IsActive(now) {
		t.Fatal("future ExpiresAt must stay active")
	}
}

func TestShouldSupersedePreferenceTopic(t *testing.T) {
	existing := Item{
		ID:     "old",
		Kind:   KindPreference,
		Scope:  ScopeGlobal,
		Status: StatusActive,
		Text:   "User prefers table-driven tests in Go packages",
	}
	candidate := Item{
		ID:    "new",
		Kind:  KindPreference,
		Scope: ScopeGlobal,
		Text:  "User prefers example-based tests instead of tables",
	}
	if !shouldSupersede(existing, candidate) {
		t.Fatalf("related preference statements should supersede (overlap=%v shared=%d)",
			tokenOverlap(existing.Text, candidate.Text), sharedTokenCount(existing.Text, candidate.Text))
	}
	// Identical text must not supersede.
	near := Item{
		ID:    "near",
		Kind:  KindPreference,
		Scope: ScopeGlobal,
		Text:  "User prefers table-driven tests in Go packages",
	}
	if shouldSupersede(existing, near) {
		t.Fatal("exact duplicate must not supersede")
	}
	// High-overlap rewrite should merge, not supersede.
	rewrite := Item{
		ID:    "rewrite",
		Kind:  KindPreference,
		Scope: ScopeGlobal,
		Text:  "User prefers table-driven tests inside Go packages always",
	}
	if shouldSupersede(existing, rewrite) {
		t.Fatal("high-overlap rewrite must not supersede")
	}
	// Facts do not supersede.
	fact := candidate
	fact.Kind = KindFact
	if shouldSupersede(existing, fact) {
		t.Fatal("facts must not supersede preferences")
	}
}

func TestApplyGovernanceOnWriteMarksSuperseded(t *testing.T) {
	now := time.Now()
	old := Item{
		ID:      "pref-old",
		Kind:    KindConstraint,
		Scope:   ScopeProject,
		Status:  StatusActive,
		Version: 1,
		Text:    "Always run the full unit_tests package before shipping",
	}
	cand := Item{
		ID:     "pref-new",
		Kind:   KindConstraint,
		Scope:  ScopeProject,
		Status: StatusActive,
		Text:   "Only run targeted unit_tests filters before shipping on Windows",
	}
	next, updates := applyGovernanceOnWrite([]Item{old}, cand, now)
	if len(updates) != 1 {
		t.Fatalf("expected one superseded update (overlap=%v shared=%d), got %#v",
			tokenOverlap(old.Text, cand.Text), sharedTokenCount(old.Text, cand.Text), updates)
	}
	if updates[0].Status != StatusSuperseded || updates[0].SupersededBy != "pref-new" {
		t.Fatalf("superseded mark = %#v", updates[0])
	}
	if next.Version < 2 {
		t.Fatalf("candidate version should bump, got %d", next.Version)
	}
	if next.Supersedes != "pref-old" {
		t.Fatalf("Supersedes = %q", next.Supersedes)
	}
}

func TestRetrieveSkipsSupersededAndExpired(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	now := time.Now()
	active := NewItem("Build with go test ./internal/memory", TierLongTerm, "s1")
	active.Kind = KindConstraint
	active.Tags = []string{"build", "memory"}
	if _, err := store.Save(ctx, active); err != nil {
		t.Fatal(err)
	}
	old := NewItem("Build with go test ./internal/app only", TierLongTerm, "s1")
	old.Kind = KindConstraint
	old.Tags = []string{"build"}
	old = MarkSuperseded(old, active.ID, now)
	if _, err := store.Save(ctx, old); err != nil {
		t.Fatal(err)
	}
	expired := NewItem("Temporary build flag for memory tests", TierShortTerm, "s1")
	expired.Kind = KindFact
	expired.ExpiresAt = now.Add(-time.Hour)
	if _, err := store.Save(ctx, expired); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(store, nil, nil)
	got, err := mgr.Retrieve(ctx, "build memory tests", "s1", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got {
		if item.ID == old.ID {
			t.Fatalf("superseded item leaked into retrieve: %#v", item)
		}
		if item.ID == expired.ID {
			t.Fatalf("expired item leaked into retrieve: %#v", item)
		}
	}
	foundActive := false
	for _, item := range got {
		if item.ID == active.ID {
			foundActive = true
		}
	}
	if !foundActive {
		t.Fatalf("active item missing from retrieve: %#v", got)
	}
}

func TestIsCoreCandidateSkipsInactive(t *testing.T) {
	item := Item{
		Kind:   KindPreference,
		Tier:   TierLongTerm,
		Scope:  ScopeGlobal,
		Status: StatusSuperseded,
		Text:   "User prefers concise replies",
	}
	if IsCoreCandidate(item) {
		t.Fatal("superseded preference must not be core")
	}
	item.Status = StatusActive
	if !IsCoreCandidate(item) {
		t.Fatal("active preference should be core")
	}
}

func TestDetectSemanticConflictPolarity(t *testing.T) {
	a := Item{Kind: KindConstraint, Scope: ScopeProject, Status: StatusActive, Text: "Always enable plan mode for large refactors"}
	b := Item{Kind: KindConstraint, Scope: ScopeProject, Status: StatusActive, Text: "Never enable plan mode for large refactors"}
	if !detectSemanticConflict(a, b) {
		t.Fatal("always vs never on shared topic should conflict")
	}
	if !shouldContradict(a, b) && !shouldSupersede(a, b) {
		// Preference/constraint with opposing polarity may supersede or contradict.
		t.Fatal("opposing polarity constraints should supersede or contradict")
	}
	same := Item{Kind: KindConstraint, Scope: ScopeProject, Status: StatusActive, Text: "Always enable plan mode for large refactors"}
	if detectSemanticConflict(a, same) {
		t.Fatal("identical polarity must not conflict")
	}
}

func TestHintMatchesExistingSupersedes(t *testing.T) {
	old := Item{ID: "abc123", Kind: KindPreference, Scope: ScopeGlobal, Status: StatusActive, Text: "Prefer dark theme in the TUI"}
	cand := Item{ID: "new", Kind: KindPreference, Scope: ScopeGlobal, Status: StatusActive, Text: "Prefer light theme in the TUI", Supersedes: "abc123"}
	if !shouldSupersede(old, cand) {
		t.Fatal("explicit supersedes id must force supersede")
	}
}

func TestGCDeletesExpiredAndKeepsSuperseded(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	now := time.Now()

	active := NewItem("Keep this active memory about retrieval budgets", TierLongTerm, "s1")
	if _, err := store.Save(ctx, active); err != nil {
		t.Fatal(err)
	}
	expired := NewItem("Temporary flag for memory GC experiment only", TierShortTerm, "s1")
	expired.ExpiresAt = now.Add(-time.Hour)
	if _, err := store.Save(ctx, expired); err != nil {
		t.Fatal(err)
	}
	superseded := NewItem("Old preference about table driven tests only", TierLongTerm, "s1")
	superseded = MarkSuperseded(superseded, active.ID, now.Add(-time.Hour))
	if _, err := store.Save(ctx, superseded); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(store, nil, nil)
	stats, err := mgr.GC(ctx, GCOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Expired < 1 {
		t.Fatalf("expected expired delete, stats=%#v", stats)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, item := range items {
		ids[item.ID] = true
	}
	if !ids[active.ID] {
		t.Fatal("active item should remain")
	}
	if ids[expired.ID] {
		t.Fatal("expired item should be hard-deleted")
	}
	if !ids[superseded.ID] {
		t.Fatal("superseded item should remain for audit by default")
	}

	// Optional retention window eventually deletes superseded.
	stats, err = mgr.GC(ctx, GCOptions{Now: now, DeleteSupersededAfter: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Superseded < 1 {
		t.Fatalf("expected superseded delete after retention, stats=%#v", stats)
	}
	items, err = store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == superseded.ID {
			t.Fatal("superseded should be gone after retention GC")
		}
	}
}

func TestConsolidateRunsExpiredGC(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	now := time.Now()
	item := NewItem("Ephemeral consolidate GC candidate for tests only", TierSensory, "s1")
	item.ExpiresAt = now.Add(-2 * time.Hour)
	item.Status = StatusActive
	if _, err := store.Save(ctx, item); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(store, nil, nil)
	if err := mgr.Consolidate(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range items {
		if got.ID == item.ID {
			t.Fatalf("consolidate should GC expired item, still have %#v", got)
		}
	}
}

func TestParseOrganizerGovernanceFieldsStored(t *testing.T) {
	// End-to-end: organizer candidate status/supersedes reach the store via
	// RememberOrganizerCandidate. Uses memory package only (no app import).
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	mgr := NewManager(store, nil, nil)

	old := NewItem("Prefer dark theme colors in the terminal UI", TierLongTerm, "s1")
	old.Kind = KindPreference
	old.Scope = ScopeGlobal
	if _, err := store.Save(ctx, old); err != nil {
		t.Fatal(err)
	}

	outcome, err := mgr.RememberOrganizerCandidate(ctx, OrganizerCandidateInput{
		Text:            "Prefer light theme colors in the terminal UI",
		Kind:            KindPreference,
		Scope:           ScopeGlobal,
		Tier:            TierLongTerm,
		Confidence:      0.9,
		Reason:          "user changed preference",
		SourceSessionID: "s1",
		Status:          "active",
		Supersedes:      old.ID,
		Model:           "test-org",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Stored {
		t.Fatalf("expected store, got %#v", outcome)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var foundOld, foundNew bool
	for _, item := range items {
		if item.ID == old.ID {
			foundOld = true
			if item.Status != StatusSuperseded {
				t.Fatalf("old status = %s, want superseded", item.Status)
			}
			if item.SupersededBy != outcome.Item.ID {
				t.Fatalf("SupersededBy = %q, want %q", item.SupersededBy, outcome.Item.ID)
			}
		}
		if item.ID == outcome.Item.ID {
			foundNew = true
			if item.Status != StatusActive {
				t.Fatalf("new status = %s", item.Status)
			}
		}
	}
	if !foundOld || !foundNew {
		t.Fatalf("missing items after organizer write: old=%v new=%v items=%#v", foundOld, foundNew, items)
	}
}
