package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type GateDecision struct {
	Allow        bool
	NeedsAI      bool
	RejectReason string
}

type Gate interface {
	Evaluate(text string, explicit bool) GateDecision
}

type Judge interface {
	JudgeMemory(ctx context.Context, input MemoryJudgementInput) (MemoryJudgement, error)
}

type Extractor interface {
	ExtractMemories(ctx context.Context, input ExtractionInput) ([]MemoryJudgement, error)
}

type MemoryJudgementInput struct {
	Text            string
	SourceSessionID string
	WorkDir         string
	ExistingSummary string
	Explicit        bool
	CandidateReason string
	RelatedMemories []Item
}

type MemoryJudgement struct {
	ShouldStore   bool     `json:"should_store"`
	Kind          Kind     `json:"kind"`
	Scope         Scope    `json:"scope"`
	SuggestedTier Tier     `json:"suggested_tier"`
	Confidence    float64  `json:"confidence"`
	CanonicalText string   `json:"canonical_text"`
	Tags          []string `json:"tags"`
	Reason        string   `json:"reason"`
}

type ExtractionInput struct {
	SourceSessionID     string
	WorkDir             string
	PreviousSummary     string
	NewSummary          string
	Transcript          string
	OriginalTranscript  string
	CompactedTranscript string
	RetainedTranscript  string
	DiscardedTranscript string
	TriggerReason       string
	EstimatedTokens     int
}

const memoryWorkingSetLimit = 10

type Manager struct {
	Store           *FileStore
	Gate            Gate
	Judge           Judge
	Extractor       Extractor
	Lifecycle       Lifecycle
	Retriever       LayeredRetriever
	RetrieveM2Limit int
	RetrieveM3Limit int
	RetrieveM4Limit int
	RetrieveM5Limit int
}

func NewManager(store *FileStore, gate Gate, judge Judge) *Manager {
	if gate == nil {
		gate = DefaultGate{}
	}
	return &Manager{Store: store, Gate: gate, Judge: judge, Lifecycle: DefaultLifecycle(), RetrieveM2Limit: 4, RetrieveM3Limit: 3, RetrieveM4Limit: 3, RetrieveM5Limit: 2}
}

func NewManagerWithExtractor(store *FileStore, gate Gate, judge Judge, extractor Extractor) *Manager {
	if gate == nil {
		gate = DefaultGate{}
	}
	return &Manager{Store: store, Gate: gate, Judge: judge, Extractor: extractor, Lifecycle: DefaultLifecycle(), RetrieveM2Limit: 4, RetrieveM3Limit: 3, RetrieveM4Limit: 3, RetrieveM5Limit: 2}
}

func (m *Manager) WithLifecycle(lifecycle Lifecycle) *Manager {
	if m == nil {
		return nil
	}
	m.Lifecycle = lifecycle.Normalize()
	return m
}

func (m *Manager) WithRetrievalBudget(m2, m3, m4, m5 int) *Manager {
	if m == nil {
		return nil
	}
	if m2 > 0 {
		m.RetrieveM2Limit = m2
	}
	if m3 > 0 {
		m.RetrieveM3Limit = m3
	}
	if m4 > 0 {
		m.RetrieveM4Limit = m4
	}
	if m5 > 0 {
		m.RetrieveM5Limit = m5
	}
	return m
}

func (m *Manager) RememberExplicit(ctx context.Context, text, sourceSessionID, workDir, existingSummary string) (Item, bool, error) {
	return m.remember(ctx, text, sourceSessionID, workDir, existingSummary, true, "explicit")
}

func (m *Manager) RememberCandidate(ctx context.Context, text, sourceSessionID, workDir, existingSummary string) (Item, bool, error) {
	return m.remember(ctx, text, sourceSessionID, workDir, existingSummary, false, "candidate")
}

func (m *Manager) remember(ctx context.Context, text, sourceSessionID, workDir, existingSummary string, explicit bool, reason string) (Item, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Item{}, false, nil
	}
	if m == nil || m.Store == nil {
		return Item{}, false, fmt.Errorf("memory manager store is nil")
	}
	decision := DefaultGate{}.Evaluate(text, explicit)
	if m.Gate != nil {
		decision = m.Gate.Evaluate(text, explicit)
	}
	if !decision.Allow {
		return Item{}, false, nil
	}
	items, err := m.Store.List(ctx)
	if err != nil {
		return Item{}, false, err
	}
	workingItems, err := m.limitWorkingSet(ctx, text, sourceSessionID, items)
	if err != nil {
		return Item{}, false, err
	}
	var related []Item
	for _, item := range workingItems {
		if item.SourceSessionID == sourceSessionID || normalizeText(item.Text) == normalizeText(text) {
			related = append(related, item)
		}
		if !shouldMergeCandidate(item, Item{Text: text, SourceSessionID: sourceSessionID}) {
			continue
		}
		item = mergeItems(item, Item{Text: text, SourceSessionID: sourceSessionID}, time.Now())
		item = m.lifecycle().Apply(item, time.Now())
		updated, err := m.Store.Save(ctx, item)
		return updated, false, err
	}
	judgement := MemoryJudgement{
		ShouldStore:   true,
		Kind:          KindFact,
		Scope:         ScopeProject,
		SuggestedTier: TierShortTerm,
		Confidence:    0.7,
		CanonicalText: text,
		Reason:        "default memory judgement",
	}
	if decision.NeedsAI && m.Judge != nil {
		judgement, err = m.Judge.JudgeMemory(ctx, MemoryJudgementInput{
			Text:            text,
			SourceSessionID: sourceSessionID,
			WorkDir:         workDir,
			ExistingSummary: existingSummary,
			Explicit:        explicit,
			CandidateReason: reason,
			RelatedMemories: related,
		})
		if err != nil {
			return Item{}, false, err
		}
	}
	if !judgement.ShouldStore {
		return Item{}, false, nil
	}
	canonicalText := strings.TrimSpace(judgement.CanonicalText)
	if canonicalText == "" {
		canonicalText = text
	}
	item := NewItem(canonicalText, judgement.SuggestedTier, sourceSessionID)
	item.Kind = nonEmptyKind(judgement.Kind, KindFact)
	item.Scope = nonEmptyScope(judgement.Scope, ScopeProject)
	item.Confidence = judgement.Confidence
	if item.Confidence <= 0 {
		item.Confidence = 0.7
	}
	item.Tags = append([]string(nil), judgement.Tags...)
	item.JudgeReason = strings.TrimSpace(judgement.Reason)
	item.JudgeModel = "memory-judge"
	item.JudgeVersion = "v1"
	if explicit && item.Tier == TierSensory {
		item.Tier = TierShortTerm
	}
	for _, existing := range related {
		if shouldMergeCandidate(existing, item) {
			merged := mergeItems(existing, item, time.Now())
			merged = m.lifecycle().Apply(merged, time.Now())
			updated, err := m.Store.Save(ctx, merged)
			return updated, false, err
		}
	}
	item = m.lifecycle().Apply(item, time.Now())
	created, err := m.Store.Save(ctx, item)
	return created, true, err
}

func (m *Manager) Retrieve(ctx context.Context, query, currentSessionID string, allowCrossSession bool, limit int) ([]Item, error) {
	if m == nil || m.Store == nil {
		return nil, nil
	}
	items, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	selected := m.retriever().Retrieve(items, RetrievalPlan{
		Query:             query,
		SessionID:         currentSessionID,
		AllowCrossSession: allowCrossSession,
		TotalLimit:        limit,
		M2Limit:           m.RetrieveM2Limit,
		M3Limit:           m.RetrieveM3Limit,
		M4Limit:           m.RetrieveM4Limit,
		M5Limit:           m.RetrieveM5Limit,
	})
	cleaned := make([]Item, 0, len(selected))
	for _, item := range selected {
		next, _, keep := sanitizeStoredMemoryItem(item)
		if !keep {
			continue
		}
		cleaned = append(cleaned, next)
	}
	return cleaned, nil
}

func (m *Manager) RememberExtracted(ctx context.Context, input ExtractionInput) ([]Item, error) {
	if m == nil || m.Store == nil {
		return nil, nil
	}
	judgements := extractToolTraceMemories(input)
	if m.Extractor != nil {
		extracted, err := m.Extractor.ExtractMemories(ctx, input)
		if err != nil && len(judgements) == 0 {
			return nil, err
		}
		if err == nil {
			judgements = append(judgements, extracted...)
		}
	}
	stored := make([]Item, 0, len(judgements))
	existingItems, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	workingItems, err := m.limitWorkingSet(ctx, input.Transcript+"\n"+input.CompactedTranscript, input.SourceSessionID, existingItems)
	if err != nil {
		return nil, err
	}
	for _, judgement := range judgements {
		if !judgement.ShouldStore {
			continue
		}
		text := strings.TrimSpace(judgement.CanonicalText)
		if text == "" {
			continue
		}
		item := NewItem(text, judgement.SuggestedTier, input.SourceSessionID)
		item.Kind = nonEmptyKind(judgement.Kind, KindFact)
		item.Scope = nonEmptyScope(judgement.Scope, ScopeProject)
		item.Confidence = judgement.Confidence
		if item.Confidence <= 0 {
			item.Confidence = 0.7
		}
		item.Tags = append([]string(nil), judgement.Tags...)
		item.JudgeReason = strings.TrimSpace(judgement.Reason)
		item.JudgeModel = "memory-extractor"
		item.JudgeVersion = "v1"
		item.DerivedFromSummary = true
		if item.Tier == TierLongTerm {
			item.Tier = TierShortTerm
		}
		mergedExisting := false
		for _, existing := range workingItems {
			if !shouldMergeCandidate(existing, item) {
				continue
			}
			merged := mergeItems(existing, item, time.Now())
			merged = m.lifecycle().Apply(merged, time.Now())
			created, err := m.Store.Save(ctx, merged)
			if err != nil {
				return stored, err
			}
			stored = append(stored, created)
			mergedExisting = true
			break
		}
		if mergedExisting {
			continue
		}
		item = m.lifecycle().Apply(item, time.Now())
		created, err := m.Store.Save(ctx, item)
		if err != nil {
			return stored, err
		}
		stored = append(stored, created)
		workingItems = append(workingItems, created)
	}
	return stored, nil
}

func (m *Manager) limitWorkingSet(ctx context.Context, query, sourceSessionID string, items []Item) ([]Item, error) {
	_ = sourceSessionID
	if len(items) <= memoryWorkingSetLimit {
		return items, nil
	}
	sorted := sortByTierRelevance(items, analyzeRetrievalQuery(query))
	selected := append([]Item(nil), sorted[:memoryWorkingSetLimit]...)
	selectedIDs := itemIDSet(selected)
	for _, item := range items {
		if selectedIDs[item.ID] {
			continue
		}
		downgraded := downgradeMemoryItem(item)
		if downgraded.Tier == item.Tier {
			continue
		}
		if _, err := m.Store.Save(ctx, downgraded); err != nil {
			return selected, err
		}
	}
	return selected, nil
}

func itemIDSet(items []Item) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item.ID] = true
	}
	return set
}

func downgradeMemoryItem(item Item) Item {
	switch item.Tier {
	case TierProcedural:
		item.Tier = TierLongTerm
	case TierLongTerm:
		item.Tier = TierShortTerm
	case TierShortTerm:
		item.Tier = TierWorking
	case TierWorking:
		item.Tier = TierSensory
	}
	item.UpdatedAt = time.Now()
	return item
}

func (m *Manager) Consolidate(ctx context.Context) error {
	if m == nil || m.Store == nil {
		return nil
	}
	items, err := m.Store.List(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, item := range items {
		if m.lifecycle().ShouldDelete(item, now) {
			if err := m.Store.Delete(ctx, item.ID); err != nil {
				return err
			}
			continue
		}
		next := m.lifecycle().Apply(item, now)
		if next.Tier != item.Tier || next.RetentionScore != item.RetentionScore || next.PromotionCount != item.PromotionCount {
			if _, err := m.Store.Save(ctx, next); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) lifecycle() Lifecycle {
	if m == nil {
		return DefaultLifecycle()
	}
	return m.Lifecycle.Normalize()
}

func (m *Manager) retriever() LayeredRetriever {
	if m == nil {
		return LayeredRetriever{}
	}
	return m.Retriever
}

type DefaultGate struct{}

func (DefaultGate) Evaluate(text string, explicit bool) GateDecision {
	text = strings.TrimSpace(text)
	if text == "" {
		return GateDecision{Allow: false, RejectReason: "empty"}
	}
	if LooksSensitive(text) {
		return GateDecision{Allow: false, RejectReason: "sensitive"}
	}
	if explicit {
		return GateDecision{Allow: true, NeedsAI: true}
	}
	return GateDecision{Allow: true, NeedsAI: false}
}

type StaticJudge struct{}

func (StaticJudge) JudgeMemory(ctx context.Context, input MemoryJudgementInput) (MemoryJudgement, error) {
	_ = ctx
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return MemoryJudgement{ShouldStore: false}, nil
	}
	return MemoryJudgement{
		ShouldStore:   true,
		Kind:          KindFact,
		Scope:         ScopeProject,
		SuggestedTier: TierShortTerm,
		Confidence:    0.7,
		CanonicalText: text,
		Reason:        "static fallback judgement",
	}, nil
}

func nonEmptyKind(value Kind, fallback Kind) Kind {
	if strings.TrimSpace(string(value)) == "" {
		return fallback
	}
	return value
}

func nonEmptyScope(value Scope, fallback Scope) Scope {
	if strings.TrimSpace(string(value)) == "" {
		return fallback
	}
	return value
}

func shouldMergeCandidate(existing Item, candidate Item) bool {
	if normalizeText(existing.Text) == normalizeText(candidate.Text) {
		return true
	}
	if existing.Kind != "" && candidate.Kind != "" && existing.Kind != candidate.Kind {
		return false
	}
	if existing.Scope != "" && candidate.Scope != "" && existing.Scope != candidate.Scope {
		return false
	}
	overlap := tokenOverlap(existing.Text, candidate.Text)
	if overlap >= 0.5 {
		return true
	}
	return sharedTokenCount(existing.Text, candidate.Text) >= 2
}

func mergeItems(existing Item, candidate Item, now time.Time) Item {
	if now.IsZero() {
		now = time.Now()
	}
	if strings.TrimSpace(candidate.Text) != "" && len(candidate.Text) > len(existing.Text) {
		existing.Text = candidate.Text
	}
	existing.AccessCount += maxInt(1, candidate.AccessCount)
	existing.LastAccessedAt = now
	existing.LastReinforcedAt = now
	existing.UpdatedAt = now
	if candidate.Importance > existing.Importance {
		existing.Importance = candidate.Importance
	}
	if candidate.Confidence > existing.Confidence {
		existing.Confidence = candidate.Confidence
	}
	if candidate.RetentionScore > existing.RetentionScore {
		existing.RetentionScore = candidate.RetentionScore
	}
	if existing.Kind == "" {
		existing.Kind = candidate.Kind
	}
	if existing.Scope == "" {
		existing.Scope = candidate.Scope
	}
	existing.Tags = mergeTags(existing.Tags, candidate.Tags)
	existing.DerivedFromSummary = existing.DerivedFromSummary || candidate.DerivedFromSummary
	if existing.SourceSessionID == "" {
		existing.SourceSessionID = candidate.SourceSessionID
	}
	if strings.TrimSpace(candidate.JudgeReason) != "" {
		existing.JudgeReason = candidate.JudgeReason
		existing.JudgeModel = candidate.JudgeModel
		existing.JudgeVersion = candidate.JudgeVersion
	}
	return existing
}

func mergeTags(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, tag := range list {
			tag = strings.TrimSpace(tag)
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

func tokenOverlap(a, b string) float64 {
	at := queryTerms(a)
	bt := queryTerms(b)
	if len(at) == 0 || len(bt) == 0 {
		return 0
	}
	setA := map[string]bool{}
	for _, t := range at {
		setA[t] = true
	}
	intersect := 0
	union := len(setA)
	setBSeen := map[string]bool{}
	for _, t := range bt {
		if setA[t] {
			intersect++
		} else if !setBSeen[t] {
			union++
		}
		setBSeen[t] = true
	}
	if union == 0 {
		return 0
	}
	return float64(intersect) / float64(union)
}

func sharedTokenCount(a, b string) int {
	setA := map[string]bool{}
	for _, t := range queryTerms(a) {
		setA[t] = true
	}
	count := 0
	seen := map[string]bool{}
	for _, t := range queryTerms(b) {
		if setA[t] && !seen[t] {
			count++
			seen[t] = true
		}
	}
	return count
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
