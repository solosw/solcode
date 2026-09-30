package memory

import (
	"strings"
	"testing"
	"time"
)

func TestIsCoreCandidatePrefersPreferencesAndConstraints(t *testing.T) {
	pref := Item{Text: "User prefers table-driven tests.", Kind: KindPreference, Tier: TierLongTerm, Confidence: 0.9}
	if !IsCoreCandidate(pref) {
		t.Fatal("preference M4 should be core")
	}
	scratch := Item{Text: "temp note", Kind: KindTask, Tier: TierSensory}
	if IsCoreCandidate(scratch) {
		t.Fatal("sensory task must not be core")
	}
	globalFact := Item{Text: "Deploy region is us-west-2 for production.", Kind: KindFact, Scope: ScopeGlobal, Tier: TierLongTerm, Confidence: 0.9}
	if !IsCoreCandidate(globalFact) {
		t.Fatal("high-confidence global fact should be core")
	}
}

func TestSelectCoreBlocksGroupsAndBounds(t *testing.T) {
	now := time.Now()
	items := []Item{
		{ID: "1", Text: "Prefer concise replies in Chinese.", Kind: KindPreference, Scope: ScopeGlobal, Tier: TierLongTerm, Confidence: 0.95, UpdatedAt: now},
		{ID: "2", Text: "Never edit generated protobuf files.", Kind: KindConstraint, Scope: ScopeProject, Tier: TierLongTerm, Confidence: 0.9, UpdatedAt: now},
		{ID: "3", Text: "Build with go test ./...", Kind: KindWorkflow, Scope: ScopeProject, Tier: TierProcedural, Confidence: 0.85, UpdatedAt: now},
		{ID: "4", Text: "scratch", Kind: KindTask, Tier: TierWorking, Confidence: 0.5, UpdatedAt: now},
	}
	blocks := SelectCoreBlocks(items, CoreSelectionOptions{MaxTotalItems: 3})
	if len(blocks) == 0 {
		t.Fatal("expected core blocks")
	}
	text := FormatCoreBlocks(blocks)
	if !strings.Contains(text, "Core memory") {
		t.Fatalf("format = %q", text)
	}
	if !strings.Contains(text, "concise replies") {
		t.Fatalf("missing preference: %q", text)
	}
	if strings.Contains(text, "scratch") {
		t.Fatalf("scratch leaked into core: %q", text)
	}
	if n := len(CoreItems(blocks)); n == 0 || n > 3 {
		t.Fatalf("core items = %d", n)
	}
}
