package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/philippgille/chromem-go"
)

func TestRememberOrganizerCandidateMergesVectorNeighbor(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "memories"))
	mgr := NewManager(store, DefaultGate{}, StaticJudge{})

	existing, err := store.Save(context.Background(), NewItem(
		"Prefer table driven Go tests for packages under internal/memory.",
		TierLongTerm,
		"s1",
	))
	if err != nil {
		t.Fatal(err)
	}
	existing.Kind = KindPreference
	existing.Scope = ScopeProject
	existing, err = store.Save(context.Background(), existing)
	if err != nil {
		t.Fatal(err)
	}

	idx := &stubVectorIndex{hits: []chromem.Result{{ID: existing.ID, Similarity: 0.92}}}
	mgr.Vectors = idx

	out, err := mgr.RememberOrganizerCandidate(context.Background(), OrganizerCandidateInput{
		Text:             "The project prefers table-driven tests in the memory package.",
		Kind:             KindPreference,
		Scope:            ScopeProject,
		Tier:             TierLongTerm,
		Confidence:       0.9,
		SourceSessionID:  "s1",
		SourceTurn:       4,
		SessionMemoryRef: FormatSessionMemoryRef("s1", 4),
		Model:            "test-org",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Merged || out.MergedID != existing.ID {
		t.Fatalf("expected vector-assisted merge, got %#v", out)
	}
	if out.Item.SessionMemoryRef != "s1#4" {
		t.Fatalf("session_memory_ref = %q", out.Item.SessionMemoryRef)
	}
}

func TestFormatSessionMemoryRef(t *testing.T) {
	if got := FormatSessionMemoryRef("main", 3); got != "main#3" {
		t.Fatalf("got %q", got)
	}
	if got := FormatSessionMemoryRef("  ", 1); got != "" {
		t.Fatalf("empty session should yield empty ref, got %q", got)
	}
}
