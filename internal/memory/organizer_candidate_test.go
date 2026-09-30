package memory

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRememberOrganizerCandidateMergesNearDuplicates(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "memories"))
	mgr := NewManager(store, DefaultGate{}, StaticJudge{})

	first, err := mgr.RememberOrganizerCandidate(context.Background(), OrganizerCandidateInput{
		Text:            "The project keeps the memory organizer fully local with no remote fallback.",
		Kind:            KindPreference,
		Scope:           ScopeProject,
		Tier:            TierLongTerm,
		Confidence:      0.9,
		Reason:          "project rule",
		SourceSessionID: "s1",
		Model:           "test-org",
	})
	if err != nil || !first.Stored || first.Merged {
		t.Fatalf("first = %#v err=%v", first, err)
	}

	second, err := mgr.RememberOrganizerCandidate(context.Background(), OrganizerCandidateInput{
		Text:            "The project keeps the memory organizer fully local with no remote fallback.",
		Kind:            KindPreference,
		Scope:           ScopeProject,
		Tier:            TierLongTerm,
		Confidence:      0.95,
		Reason:          "repeat",
		SourceSessionID: "s1",
		Model:           "test-org",
	})
	if err != nil {
		t.Fatalf("second err = %v", err)
	}
	if !second.Merged || second.MergedID != first.Item.ID {
		t.Fatalf("expected merge into first, got %#v", second)
	}

	items, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 after merge", len(items))
	}
}

func TestRememberOrganizerCandidateDowngradesPlainFactFromM4(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "memories"))
	mgr := NewManager(store, DefaultGate{}, StaticJudge{})

	out, err := mgr.RememberOrganizerCandidate(context.Background(), OrganizerCandidateInput{
		Text:       "Build command is go test ./internal/memory/.",
		Kind:       KindFact,
		Scope:      ScopeProject,
		Tier:       TierLongTerm,
		Confidence: 0.8,
	})
	if err != nil || !out.Stored {
		t.Fatalf("outcome = %#v err=%v", out, err)
	}
	if out.Item.Tier != TierShortTerm {
		t.Fatalf("tier = %q, want M3 for plain fact from organizer", out.Item.Tier)
	}
}
