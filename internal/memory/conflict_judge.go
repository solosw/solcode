package memory

import (
	"context"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/systemone"
)

// ConflictVerdict is how a pair of memories should relate under governance.
type ConflictVerdict string

const (
	// ConflictNone: unrelated or compatible — no governance edge.
	ConflictNone ConflictVerdict = "none"
	// ConflictSupersede: candidate replaces existing (policy update).
	ConflictSupersede ConflictVerdict = "supersede"
	// ConflictContradict: unresolved conflict; keep both linked.
	ConflictContradict ConflictVerdict = "contradict"
	// ConflictSame: near-duplicate / rewrite — prefer merge, not conflict.
	ConflictSame ConflictVerdict = "same"
)

// ConflictJudge decides supersede / contradict / same / none for one pair.
// Implementations must be total: on failure return heuristic fallback, never
// error that aborts a write.
type ConflictJudge interface {
	Adjudicate(ctx context.Context, existing, candidate Item) ConflictVerdict
}

// HeuristicConflictJudge is the polarity + token-overlap path.
type HeuristicConflictJudge struct{}

func (HeuristicConflictJudge) Adjudicate(_ context.Context, existing, candidate Item) ConflictVerdict {
	return heuristicConflictVerdict(existing, candidate)
}

func heuristicConflictVerdict(existing, candidate Item) ConflictVerdict {
	if !existing.IsActive(time.Time{}) {
		return ConflictNone
	}
	if existing.ID != "" && candidate.ID != "" && existing.ID == candidate.ID {
		return ConflictNone
	}
	if normalizeText(existing.Text) == normalizeText(candidate.Text) {
		return ConflictSame
	}
	// Explicit supersede hint always wins.
	if hintMatchesExisting(candidate.Supersedes, existing) {
		switch candidate.Kind {
		case KindPreference, KindConstraint, KindFact, KindWorkflow:
			return ConflictSupersede
		}
	}
	// Policy change / polarity conflict before near-duplicate merge. shouldMerge
	// is intentionally loose (shared>=2) and would otherwise swallow supersedes.
	if shouldSupersede(existing, candidate) {
		return ConflictSupersede
	}
	if shouldContradict(existing, candidate) {
		return ConflictContradict
	}
	// Only treat high-overlap rewrites as "same" once supersede/contradict are out.
	overlap := tokenOverlap(existing.Text, candidate.Text)
	if overlap >= 0.55 {
		return ConflictSame
	}
	if shouldMergeCandidate(existing, candidate) && overlap >= 0.4 {
		return ConflictSame
	}
	return ConflictNone
}

// JevConflictJudge asks System One to classify the relationship when the pair
// looks related. High-confidence explicit hints short-circuit without a call.
// When Jev is off or uncertain, falls back to HeuristicConflictJudge.
type JevConflictJudge struct {
	Decider       *systemone.Decider
	MinConfidence float64
	// Fallback used when Jev is off or uncertain.
	Fallback ConflictJudge
}

func (j JevConflictJudge) fallback() ConflictJudge {
	if j.Fallback != nil {
		return j.Fallback
	}
	return HeuristicConflictJudge{}
}

func (j JevConflictJudge) minConfidence() float64 {
	if j.MinConfidence <= 0 || j.MinConfidence > 1 {
		return 0.55
	}
	return j.MinConfidence
}

// Adjudicate implements ConflictJudge.
func (j JevConflictJudge) Adjudicate(ctx context.Context, existing, candidate Item) ConflictVerdict {
	base := j.fallback().Adjudicate(ctx, existing, candidate)

	// Identical text is never a conflict.
	if normalizeText(existing.Text) == normalizeText(candidate.Text) {
		return ConflictSame
	}
	// Explicit supersede id/topic hint stays authoritative without Jev.
	if hintMatchesExisting(candidate.Supersedes, existing) {
		switch candidate.Kind {
		case KindPreference, KindConstraint, KindFact, KindWorkflow:
			return ConflictSupersede
		}
	}

	decider := j.Decider
	if decider == nil || !decider.Enabled() {
		return base
	}

	// Spend a Jev call when the pair looks related, including cases where the
	// heuristic already guessed supersede/contradict/same — the model can
	// still refine ambiguous polarity-free rewrites.
	if !pairLooksRelated(existing, candidate) {
		return base
	}

	state := map[string]any{
		"existing_id":               existing.ID,
		"existing_text":             strings.TrimSpace(existing.Text),
		"existing_kind":             string(existing.Kind),
		"existing_scope":            string(existing.Scope),
		"existing_topic":            existing.Topic,
		"candidate_text":            strings.TrimSpace(candidate.Text),
		"candidate_kind":            string(candidate.Kind),
		"candidate_scope":           string(candidate.Scope),
		"candidate_topic":           candidate.Topic,
		"candidate_supersedes_hint": candidate.Supersedes,
		"heuristic_guess":           string(base),
	}

	choice, _ := decider.Choose(ctx, state,
		"How should the NEW candidate memory relate to the EXISTING memory? "+
			"supersede = candidate is a deliberate policy/preference update that replaces existing. "+
			"contradict = they conflict and neither clearly replaces the other. "+
			"same = they are the same fact rewritten (merge). "+
			"none = unrelated or compatible; leave both alone.",
		map[string]string{
			string(ConflictSupersede):  "Candidate replaces existing: newer rule on the same topic",
			string(ConflictContradict): "Irreconcilable conflict on the same topic; keep both linked",
			string(ConflictSame):       "Near-duplicate rewrite of the same belief; should merge",
			string(ConflictNone):       "Different topics or compatible; no governance action",
		},
		j.minConfidence(),
		string(base),
	)
	switch ConflictVerdict(strings.ToLower(strings.TrimSpace(choice))) {
	case ConflictSupersede, ConflictContradict, ConflictSame, ConflictNone:
		return ConflictVerdict(strings.ToLower(strings.TrimSpace(choice)))
	default:
		return base
	}
}

func pairLooksRelated(a, b Item) bool {
	if strings.TrimSpace(a.Topic) != "" && strings.TrimSpace(b.Topic) != "" &&
		strings.EqualFold(a.Topic, b.Topic) {
		return true
	}
	if sameTopic(a, b) {
		return true
	}
	if sharedTokenCount(a.Text, b.Text) >= 2 {
		return true
	}
	if tokenOverlap(a.Text, b.Text) >= 0.2 {
		return true
	}
	return false
}
