package unit_tests

import (
	"context"
	"testing"
	"time"

	"github.com/solosw/codeplus-agent/internal/memory"
)

type stubJudge struct {
	judgement memory.MemoryJudgement
}

func (s stubJudge) JudgeMemory(ctx context.Context, input memory.MemoryJudgementInput) (memory.MemoryJudgement, error) {
	_ = ctx
	_ = input
	return s.judgement, nil
}

type stubExtractor struct {
	judgements []memory.MemoryJudgement
	seenInput  memory.ExtractionInput
}

func (s *stubExtractor) ExtractMemories(ctx context.Context, input memory.ExtractionInput) ([]memory.MemoryJudgement, error) {
	_ = ctx
	s.seenInput = input
	return s.judgements, nil
}

func TestMemoryManagerRememberExplicitUsesJudgeResult(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())
	manager := memory.NewManager(store, memory.DefaultGate{}, stubJudge{judgement: memory.MemoryJudgement{
		ShouldStore:   true,
		Kind:          memory.KindPreference,
		Scope:         memory.ScopeGlobal,
		SuggestedTier: memory.TierLongTerm,
		Confidence:    0.93,
		CanonicalText: "User prefers concise final answers",
		Tags:          []string{"style", "answering"},
		Reason:        "stable global preference",
	}})

	item, created, err := manager.RememberExplicit(ctx, "I prefer concise final answers", "main", t.TempDir(), "")
	if err != nil {
		t.Fatalf("RememberExplicit: %v", err)
	}
	if !created {
		t.Fatal("expected created=true")
	}
	if item.Tier != memory.TierLongTerm {
		t.Fatalf("expected tier %s, got %s", memory.TierLongTerm, item.Tier)
	}
	if item.Kind != memory.KindPreference {
		t.Fatalf("expected kind %s, got %s", memory.KindPreference, item.Kind)
	}
	if item.Scope != memory.ScopeGlobal {
		t.Fatalf("expected scope %s, got %s", memory.ScopeGlobal, item.Scope)
	}
	if item.Text != "User prefers concise final answers" {
		t.Fatalf("unexpected canonical text %q", item.Text)
	}
	if item.Confidence != 0.93 {
		t.Fatalf("expected confidence 0.93, got %v", item.Confidence)
	}
}

func TestMemoryManagerRetrieveSkipsSensoryAndCrossSession(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())
	_, err := store.Save(ctx, memory.Item{
		ID:              "m1",
		Tier:            memory.TierSensory,
		Kind:            memory.KindFact,
		Scope:           memory.ScopeProject,
		Text:            "temporary idea",
		Importance:      0.7,
		Confidence:      0.7,
		RetentionScore:  0.7,
		AccessCount:     1,
		SourceSessionID: "s1",
	})
	if err != nil {
		t.Fatalf("save sensory: %v", err)
	}
	_, err = store.Save(ctx, memory.Item{
		ID:              "m2",
		Tier:            memory.TierShortTerm,
		Kind:            memory.KindFact,
		Scope:           memory.ScopeProject,
		Text:            "project constraint concise",
		Importance:      0.8,
		Confidence:      0.8,
		RetentionScore:  0.8,
		AccessCount:     1,
		SourceSessionID: "s2",
	})
	if err != nil {
		t.Fatalf("save short-term: %v", err)
	}
	_, err = store.Save(ctx, memory.Item{
		ID:              "m3",
		Tier:            memory.TierLongTerm,
		Kind:            memory.KindPreference,
		Scope:           memory.ScopeProject,
		Text:            "prefer concise answers",
		Importance:      0.9,
		Confidence:      0.9,
		RetentionScore:  0.9,
		AccessCount:     1,
		SourceSessionID: "s1",
	})
	if err != nil {
		t.Fatalf("save long-term: %v", err)
	}

	manager := memory.NewManager(store, memory.DefaultGate{}, nil)
	items, err := manager.Retrieve(ctx, "concise", "s1", false, 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].ID != "m3" {
		t.Fatalf("expected m3, got %s", items[0].ID)
	}
}

func TestLayeredRetrieverPrioritizesWorkingThenShortThenLong(t *testing.T) {
	now := time.Now()
	items := []memory.Item{
		{ID: "m2", Tier: memory.TierWorking, Kind: memory.KindTask, Text: "current concise task", Importance: 0.7, Confidence: 0.8, AccessCount: 1, UpdatedAt: now, LastAccessedAt: now, SourceSessionID: "s1"},
		{ID: "m3", Tier: memory.TierShortTerm, Kind: memory.KindConstraint, Text: "project concise constraint", Importance: 0.8, Confidence: 0.8, AccessCount: 1, UpdatedAt: now.Add(-time.Minute), LastAccessedAt: now.Add(-time.Minute), SourceSessionID: "s1"},
		{ID: "m4", Tier: memory.TierLongTerm, Kind: memory.KindPreference, Text: "prefer concise answers", Importance: 0.9, Confidence: 0.9, AccessCount: 1, UpdatedAt: now.Add(-2 * time.Minute), LastAccessedAt: now.Add(-2 * time.Minute), SourceSessionID: "s1"},
	}
	got := (memory.LayeredRetriever{}).Retrieve(items, memory.RetrievalPlan{Query: "concise", SessionID: "s1", AllowCrossSession: true, TotalLimit: 3, M2Limit: 1, M3Limit: 1, M4Limit: 1, M5Limit: 1})
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d", len(got))
	}
	if got[0].ID != "m2" || got[1].ID != "m3" || got[2].ID != "m4" {
		t.Fatalf("unexpected retrieval order: %#v", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}

func TestLayeredRetrieverBoostsProceduralOnWorkflowQuery(t *testing.T) {
	now := time.Now()
	items := []memory.Item{
		{ID: "long", Tier: memory.TierLongTerm, Kind: memory.KindPreference, Text: "prefer concise answers", Importance: 0.9, Confidence: 0.9, AccessCount: 1, UpdatedAt: now, LastAccessedAt: now, SourceSessionID: "s1"},
		{ID: "proc", Tier: memory.TierProcedural, Kind: memory.KindWorkflow, Text: "first run tests then summarize results", Importance: 0.7, Confidence: 0.9, AccessCount: 1, UpdatedAt: now.Add(-time.Minute), LastAccessedAt: now.Add(-time.Minute), SourceSessionID: "s1"},
	}
	got := (memory.LayeredRetriever{}).Retrieve(items, memory.RetrievalPlan{Query: "how to review workflow first", SessionID: "s1", AllowCrossSession: true, TotalLimit: 2, M2Limit: 1, M3Limit: 1, M4Limit: 1, M5Limit: 1})
	if len(got) == 0 || got[0].ID != "proc" {
		t.Fatalf("expected procedural memory first, got %#v", got)
	}
}

func TestLayeredRetrieverBoostsExactFileAndCodeMatches(t *testing.T) {
	now := time.Now()
	items := []memory.Item{
		{ID: "generic", Tier: memory.TierShortTerm, Kind: memory.KindTask, Text: "Compacted session tools used: Edit, Bash.", Tags: []string{"tool-usage"}, Importance: 0.9, Confidence: 0.9, AccessCount: 1, UpdatedAt: now, LastAccessedAt: now, SourceSessionID: "s1"},
		{ID: "file", Tier: memory.TierShortTerm, Kind: memory.KindTask, Text: "Compacted session file modifications: internal/session/compactor.go: edited compressMessagesWithHeadroom to preserve tool ids.", Tags: []string{"code-change", "files", "modifications"}, Importance: 0.7, Confidence: 0.8, AccessCount: 1, UpdatedAt: now.Add(-time.Minute), LastAccessedAt: now.Add(-time.Minute), SourceSessionID: "s1"},
	}
	got := (memory.LayeredRetriever{}).Retrieve(items, memory.RetrievalPlan{Query: "继续看 internal/session/compactor.go 的 compressMessagesWithHeadroom 修改", SessionID: "s1", AllowCrossSession: true, TotalLimit: 2, M2Limit: 1, M3Limit: 2, M4Limit: 1, M5Limit: 1})
	if len(got) == 0 || got[0].ID != "file" {
		t.Fatalf("expected exact file/code memory first, got %#v", got)
	}
}

func TestLayeredRetrieverBoostsValidationCommands(t *testing.T) {
	now := time.Now()
	items := []memory.Item{
		{ID: "edit", Tier: memory.TierShortTerm, Kind: memory.KindTask, Text: "internal/app/app.go: edited app startup", Tags: []string{"code-change"}, Importance: 0.9, Confidence: 0.9, AccessCount: 1, UpdatedAt: now, LastAccessedAt: now, SourceSessionID: "s1"},
		{ID: "test", Tier: memory.TierShortTerm, Kind: memory.KindTask, Text: "Compacted session validation/build commands run: go test ./internal/app ./unit_tests.", Tags: []string{"validation", "build"}, Importance: 0.7, Confidence: 0.8, AccessCount: 1, UpdatedAt: now.Add(-time.Minute), LastAccessedAt: now.Add(-time.Minute), SourceSessionID: "s1"},
	}
	got := (memory.LayeredRetriever{}).Retrieve(items, memory.RetrievalPlan{Query: "之前 go test ./internal/app ./unit_tests 的结果", SessionID: "s1", AllowCrossSession: true, TotalLimit: 2, M2Limit: 1, M3Limit: 2, M4Limit: 1, M5Limit: 1})
	if len(got) == 0 || got[0].ID != "test" {
		t.Fatalf("expected validation memory first, got %#v", got)
	}
}

func TestLayeredRetrieverUsesSegmentedVectorSimilarity(t *testing.T) {
	now := time.Now()
	items := []memory.Item{
		{ID: "loose", Tier: memory.TierShortTerm, Kind: memory.KindTask, Text: "Compacted session file modifications: internal/config/config.go: edited max token defaults and config loading.", Tags: []string{"code-change", "files"}, Importance: 0.95, Confidence: 0.9, AccessCount: 1, UpdatedAt: now, LastAccessedAt: now, SourceSessionID: "s1"},
		{ID: "vector", Tier: memory.TierShortTerm, Kind: memory.KindTask, Text: "Compacted session file modifications: internal/engine/context_builder.go: edited ContextBuilder withContextMessages memory injection and context token estimate.", Tags: []string{"code-change", "context-builder", "memory"}, Importance: 0.65, Confidence: 0.8, AccessCount: 1, UpdatedAt: now.Add(-time.Minute), LastAccessedAt: now.Add(-time.Minute), SourceSessionID: "s1"},
	}
	got := (memory.LayeredRetriever{}).Retrieve(items, memory.RetrievalPlan{Query: "上下文构建器 context builder memory injection 怎么改的", SessionID: "s1", AllowCrossSession: true, TotalLimit: 2, M2Limit: 1, M3Limit: 2, M4Limit: 1, M5Limit: 1})
	if len(got) == 0 || got[0].ID != "vector" {
		t.Fatalf("expected segmented vector-similar memory first, got %#v", got)
	}
}

func TestMemoryManagerRememberExtractedMarksDerivedAndAvoidsDirectLongTerm(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())
	extractor := &stubExtractor{judgements: []memory.MemoryJudgement{
		{
			ShouldStore:   true,
			Kind:          memory.KindConstraint,
			Scope:         memory.ScopeProject,
			SuggestedTier: memory.TierLongTerm,
			Confidence:    0.88,
			CanonicalText: "Project prefers minimal edits over refactors",
			Tags:          []string{"constraint"},
			Reason:        "durable extracted rule",
		},
	}}
	manager := memory.NewManagerWithExtractor(store, memory.DefaultGate{}, nil, extractor)
	items, err := manager.RememberExtracted(ctx, memory.ExtractionInput{
		SourceSessionID:     "s1",
		WorkDir:             t.TempDir(),
		PreviousSummary:     "old",
		NewSummary:          "new",
		Transcript:          "transcript",
		CompactedTranscript: "compressed transcript",
	})
	if err != nil {
		t.Fatalf("RememberExtracted: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 extracted memory, got %d", len(items))
	}
	if !items[0].DerivedFromSummary {
		t.Fatal("expected DerivedFromSummary=true")
	}
	if items[0].Tier != memory.TierShortTerm {
		t.Fatalf("expected extracted long-term candidate to be downgraded to M3, got %s", items[0].Tier)
	}
	if extractor.seenInput.CompactedTranscript != "compressed transcript" {
		t.Fatalf("expected compacted transcript to be forwarded to extractor, got %q", extractor.seenInput.CompactedTranscript)
	}
}

func TestMemoryManagerRememberExtractedMergesSimilarCandidates(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())
	_, err := store.Save(ctx, memory.Item{
		ID:                 "base",
		Tier:               memory.TierShortTerm,
		Kind:               memory.KindConstraint,
		Scope:              memory.ScopeProject,
		Text:               "prefer minimal edits over refactors",
		Importance:         0.7,
		Confidence:         0.75,
		RetentionScore:     0.7,
		AccessCount:        1,
		SourceSessionID:    "s1",
		DerivedFromSummary: true,
	})
	if err != nil {
		t.Fatalf("save base: %v", err)
	}
	extractor := &stubExtractor{judgements: []memory.MemoryJudgement{
		{
			ShouldStore:   true,
			Kind:          memory.KindConstraint,
			Scope:         memory.ScopeProject,
			SuggestedTier: memory.TierShortTerm,
			Confidence:    0.88,
			CanonicalText: "project prefers minimal edits instead of refactors",
			Tags:          []string{"constraint", "style"},
			Reason:        "same durable rule",
		},
	}}
	manager := memory.NewManagerWithExtractor(store, memory.DefaultGate{}, nil, extractor)
	items, err := manager.RememberExtracted(ctx, memory.ExtractionInput{SourceSessionID: "s1", WorkDir: t.TempDir(), Transcript: "x"})
	if err != nil {
		t.Fatalf("RememberExtracted merge: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected merged result len 1, got %d", len(items))
	}
	all, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 merged memory on disk, got %d", len(all))
	}
	if all[0].AccessCount < 2 {
		t.Fatalf("expected merged access count >=2, got %d", all[0].AccessCount)
	}
}

func TestLifecyclePromotesWorkflowToProcedural(t *testing.T) {
	item := memory.NewItem("Run tests before final answer", memory.TierShortTerm, "s1")
	item.Kind = memory.KindWorkflow
	got := memory.DefaultLifecycle().Apply(item, item.UpdatedAt)
	if got.Tier != memory.TierProcedural {
		t.Fatalf("expected procedural tier, got %s", got.Tier)
	}
}

func TestLifecycleDecaysWorkingMemoryToSensory(t *testing.T) {
	item := memory.NewItem("temporary working context", memory.TierWorking, "s1")
	item.LastReinforcedAt = time.Now().Add(-96 * time.Hour)
	got := memory.DefaultLifecycle().Apply(item, time.Now())
	if got.Tier != memory.TierSensory {
		t.Fatalf("expected decay to sensory, got %s", got.Tier)
	}
}

func TestMemoryManagerConsolidatePromotesShortTermToLongTerm(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())
	item := memory.NewItem("stable preference", memory.TierShortTerm, "s1")
	item.Kind = memory.KindPreference
	item.Scope = memory.ScopeGlobal
	item.AccessCount = 4
	item.Confidence = 0.9
	item.RetentionScore = 0.8
	item.CreatedAt = time.Now().Add(-48 * time.Hour)
	if _, err := store.Save(ctx, item); err != nil {
		t.Fatalf("save: %v", err)
	}
	manager := memory.NewManager(store, memory.DefaultGate{}, nil)
	if err := manager.Consolidate(ctx); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Tier != memory.TierLongTerm {
		t.Fatalf("expected promotion to M4, got %s", items[0].Tier)
	}
}

func TestMemoryManagerConsolidateDoesNotPromoteDerivedSummaryCandidateToLongTerm(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())
	item := memory.NewItem("derived summary constraint", memory.TierShortTerm, "s1")
	item.Kind = memory.KindConstraint
	item.Scope = memory.ScopeProject
	item.AccessCount = 8
	item.Confidence = 0.95
	item.RetentionScore = 0.9
	item.DerivedFromSummary = true
	item.CreatedAt = time.Now().Add(-72 * time.Hour)
	if _, err := store.Save(ctx, item); err != nil {
		t.Fatalf("save: %v", err)
	}
	manager := memory.NewManager(store, memory.DefaultGate{}, nil)
	if err := manager.Consolidate(ctx); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Tier != memory.TierShortTerm {
		t.Fatalf("expected derived summary memory to stay in M3, got %s", items[0].Tier)
	}
}

func TestMemoryManagerConsolidateKeepsWeakShortTermMemoryOutOfLongTerm(t *testing.T) {
	ctx := context.Background()
	store := memory.NewFileStore(t.TempDir())
	item := memory.NewItem("recent low-signal preference", memory.TierShortTerm, "s1")
	item.Kind = memory.KindPreference
	item.Scope = memory.ScopeProject
	item.AccessCount = 3
	item.Confidence = 0.76
	item.RetentionScore = 0.56
	item.CreatedAt = time.Now().Add(-2 * time.Hour)
	if _, err := store.Save(ctx, item); err != nil {
		t.Fatalf("save: %v", err)
	}
	manager := memory.NewManager(store, memory.DefaultGate{}, nil)
	if err := manager.Consolidate(ctx); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if items[0].Tier != memory.TierShortTerm {
		t.Fatalf("expected weak signal memory to remain in M3, got %s", items[0].Tier)
	}
}
