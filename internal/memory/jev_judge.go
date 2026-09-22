package memory

import (
	"context"
	"strconv"
	"strings"

	"github.com/solosw/solcode/internal/systemone"
)

// JevJudge classifies memories with TypeSafe System One questions instead of
// asking a chat model to emit JSON.
//
// The LLM-based judge (AnthropicJudge) asks the model to "return only JSON" and
// then parses that text, so a single stray sentence breaks the whole judgement
// and the model self-reports a confidence nobody calibrated. Jev inverts that:
// every field is a typed question whose answer cannot fall outside the options
// we supplied, and the confidence is computed from the probability
// distribution rather than asserted.
//
// Every question is asked in one request against the same state; questions are
// evaluated in parallel and the answers are correlated through the shared
// context, so the cost is one round trip regardless of how many fields we need.
type JevJudge struct {
	Decider *systemone.Decider
	// MinConfidence is the floor below which we keep the deterministic default
	// judgement instead of acting on a low-confidence classification. Default
	// 0.5: below an even split we have no real signal.
	MinConfidence float64
}

const (
	jevTierLevels = "M1 sensory scratch, M2 current-session working context, M3 project short-term, M4 stable long-term fact or preference, M5 reusable workflow or procedure"
	jevKindNote   = "kind describes what the memory is, independently of its scope or lifetime"
)

func (j JevJudge) judgeDecider() *systemone.Decider {
	if j.Decider == nil {
		return nil
	}
	return j.Decider
}

func (j JevJudge) judgeMinConfidence() float64 {
	if j.MinConfidence <= 0 || j.MinConfidence > 1 {
		return 0.5
	}
	return j.MinConfidence
}

// JudgeMemory implements Judge.
func (j JevJudge) JudgeMemory(ctx context.Context, input MemoryJudgementInput) (MemoryJudgement, error) {
	text := strings.TrimSpace(input.Text)
	fallback := StaticJudge{}.fallbackJudgement(text, input)
	if text == "" {
		return fallback, nil
	}
	decider := j.judgeDecider()
	if decider == nil || !decider.Enabled() {
		return fallback, nil
	}

	state := map[string]any{
		"text":              text,
		"source_session_id": input.SourceSessionID,
		"work_dir":          input.WorkDir,
		"existing_summary":  input.ExistingSummary,
		"related_memories":  relatedMemoryTexts(input.RelatedMemories),
		"explicit_request":  input.Explicit,
		"candidate_reason":  input.CandidateReason,
	}

	minConfidence := j.judgeMinConfidence()
	judgement := fallback

	// One Choice for the kind. Unknown or low-confidence answers keep the
	// fallback kind rather than storing a category we cannot justify.
	if kind, confidence := decider.Choose(ctx, state,
		"Which category does `text` belong to? Classify what the memory is about, not how important it is.",
		map[string]string{
			string(KindFact):       "A durable statement that is true of the project, code, or environment",
			string(KindPreference): "A user or team preference about how work should be done",
			string(KindConstraint): "A rule or limit that must not be violated",
			string(KindTask):       "Work that is pending, in progress, or planned",
			string(KindWorkflow):   "A reusable procedure or sequence of steps",
		}, minConfidence, string(fallback.Kind)); kind != "" {
		judgement.Kind = Kind(kind)
		if confidence > 0 {
			judgement.Confidence = confidence
		}
	}

	// Scope as a Choice: the options are a fixed set with no natural ordering,
	// so a Choice (not a Score) is the right primitive.
	if scope, confidence := decider.Choose(ctx, state,
		"Who should this memory apply to?",
		map[string]string{
			string(ScopeSession): "Only useful for the current session and not beyond it",
			string(ScopeProject): "Applies to this project's code, conventions, or setup",
			string(ScopeGlobal):  "Applies to the user or environment everywhere, not just this project",
		}, minConfidence, string(fallback.Scope)); scope != "" {
		judgement.Scope = Scope(scope)
		if confidence > 0 && confidence > judgement.Confidence {
			judgement.Confidence = confidence
		}
	}

	// Tier is a position on an ordered spectrum, which is exactly a Score.
	if tier, ok := j.judgeTier(ctx, state, minConfidence); ok {
		judgement.SuggestedTier = tier
	}

	// Durability is a yes/no judgment, so it is a Noul. It decides ShouldStore
	// and never overwrites the classification confidence: those are different
	// questions and averaging them into one number would hide both.
	if worthKeeping, probability := j.worthStoring(ctx, state); probability >= 0 {
		judgement.ShouldStore = worthKeeping
		judgement.Reason = "jev system one judgement (durability " + formatProbability(probability) + ")"
	}

	judgement.CanonicalText = fallback.CanonicalText
	if judgement.Reason == "" {
		judgement.Reason = "jev system one judgement"
	}
	return judgement, nil
}

// worthStoring asks whether the memory has lasting value. A "maybe" keeps the
// caller's default rather than discarding a possible memory: dropping durable
// knowledge is the more expensive mistake.
func (j JevJudge) worthStoring(ctx context.Context, state any) (bool, float64) {
	decider := j.judgeDecider()
	if decider == nil || !decider.Enabled() {
		return true, -1
	}
	keep, probability := decider.Noul(ctx, state,
		"Will this still be worth knowing in a future session, or is it transient chatter with no lasting value?",
		0.7, 0.3, true)
	return keep, probability
}

// judgeTier maps a Score answer onto the M1..M5 tier by rounding the position.
func (j JevJudge) judgeTier(ctx context.Context, state any, minConfidence float64) (Tier, bool) {
	decider := j.judgeDecider()
	if decider == nil || !decider.Enabled() {
		return "", false
	}
	levels := []string{
		"no lasting value beyond the current message",
		"current-session working context only",
		"project short-term detail useful for a while",
		"stable long-term fact, preference, or constraint",
		"reusable workflow or procedure worth keeping indefinitely",
	}
	answer, err := decider.Answer(ctx, state,
		"How long should this memory stay useful to the project?", levels)
	if err != nil {
		return "", false
	}
	if answer.Confidence < minConfidence && minConfidence > 0 {
		return "", false
	}
	tiers := []Tier{TierSensory, TierWorking, TierShortTerm, TierLongTerm, TierProcedural}
	index := int(answer.Score + 0.5)
	if index < 0 {
		index = 0
	}
	if index >= len(tiers) {
		index = len(tiers) - 1
	}
	return tiers[index], true
}

// ShouldStore asks the store/skip question as a Noul and thresholds it.
// Manager reads ShouldStore off MemoryJudgement instead; this is exposed for
// callers that want the decision without building a full judgement.
func (j JevJudge) ShouldStore(ctx context.Context, text string) (bool, float64) {
	decider := j.judgeDecider()
	if decider == nil || !decider.Enabled() {
		return true, 0
	}
	return decider.Noul(ctx, strings.TrimSpace(text),
		"Will this still be worth knowing in a future session, or is it transient chatter?",
		0.7, 0.3, true)
}

// JevExtractor extracts candidate memories with System One questions.
//
// Extraction is a genuinely different shape from judgement: a chat model can
// propose an arbitrary number of candidates, while a System One model can only
// answer questions about a state we already built. So this extractor does not
// invent memories; it validates and classifies the deterministic tool-trace
// candidates that extractToolTraceMemories already produces, keeping the ones
// worth storing. That trade removes an LLM round trip and a JSON parse from the
// compaction path while keeping every stored memory typed.
type JevExtractor struct {
	Judge JevJudge
}

// ExtractMemories implements Extractor.
func (e JevExtractor) ExtractMemories(ctx context.Context, input ExtractionInput) ([]MemoryJudgement, error) {
	candidates := extractToolTraceMemories(input)
	if len(candidates) == 0 {
		return nil, nil
	}
	judge := e.Judge
	if judge.judgeDecider() == nil || !judge.judgeDecider().Enabled() {
		return candidates, nil
	}
	out := make([]MemoryJudgement, 0, len(candidates))
	for _, candidate := range candidates {
		judgement, err := judge.JudgeMemory(ctx, MemoryJudgementInput{
			Text:            candidate.CanonicalText,
			SourceSessionID: input.SourceSessionID,
			WorkDir:         input.WorkDir,
			ExistingSummary: input.NewSummary,
			CandidateReason: "tool-trace extraction",
		})
		if err != nil {
			// Keep the deterministic candidate rather than dropping extracted
			// work because a decision service was unreachable.
			out = append(out, candidate)
			continue
		}
		if !judgement.ShouldStore {
			continue
		}
		if strings.TrimSpace(judgement.CanonicalText) == "" {
			judgement.CanonicalText = candidate.CanonicalText
		}
		out = append(out, judgement)
	}
	if len(out) == 0 {
		return candidates, nil
	}
	return out, nil
}

// formatProbability renders a probability for the judge reason string.
func formatProbability(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func relatedMemoryTexts(items []Item) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}
