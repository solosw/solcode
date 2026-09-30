package memory

import (
	"context"
	"strings"
	"time"
	"unicode"
)

// Status is the lifecycle state of an archival memory entry.
//
// Active entries participate in retrieval. Superseded / expired / contradicted
// entries remain on disk for audit but are skipped by default retrieval.
type Status string

const (
	StatusActive       Status = "active"
	StatusSuperseded   Status = "superseded"
	StatusExpired      Status = "expired"
	StatusContradicted Status = "contradicted"
)

// GCOptions bounds how aggressively inactive memories are removed.
type GCOptions struct {
	// KeepExpired, when true, only marks past-ExpiresAt items as expired and
	// does not hard-delete StatusExpired entries. Default (false) deletes them.
	KeepExpired bool
	// DeleteSupersededAfter is how long a superseded/contradicted entry is kept
	// for audit before hard-delete. Zero keeps them forever.
	DeleteSupersededAfter time.Duration
	// Now overrides the clock (tests).
	Now time.Time
}

// GCStats reports what one GC pass did.
type GCStats struct {
	Scanned   int
	Deleted   int
	Expired   int
	Superseded int
	MarkedExpired int
}

// IsActive reports whether the item should be retrieved into context.
func (item Item) IsActive(now time.Time) bool {
	status := item.normalizedStatus()
	if status != StatusActive && status != "" {
		return false
	}
	if !item.ExpiresAt.IsZero() {
		if now.IsZero() {
			now = time.Now()
		}
		if !now.Before(item.ExpiresAt) {
			return false
		}
	}
	return true
}

func (item Item) normalizedStatus() Status {
	status := Status(strings.ToLower(strings.TrimSpace(string(item.Status))))
	switch status {
	case StatusActive, StatusSuperseded, StatusExpired, StatusContradicted:
		return status
	case "":
		return StatusActive
	default:
		return StatusActive
	}
}

func normalizeIncomingStatus(value string) Status {
	switch Status(strings.ToLower(strings.TrimSpace(value))) {
	case StatusActive, StatusSuperseded, StatusExpired, StatusContradicted:
		return Status(strings.ToLower(strings.TrimSpace(value)))
	default:
		return StatusActive
	}
}

// MarkSuperseded records that this entry was replaced by newerID.
func MarkSuperseded(item Item, newerID string, now time.Time) Item {
	if now.IsZero() {
		now = time.Now()
	}
	item.Status = StatusSuperseded
	item.SupersededBy = strings.TrimSpace(newerID)
	item.UpdatedAt = now
	return item
}

// MarkContradicted records that this entry conflicts with otherID.
func MarkContradicted(item Item, otherID string, now time.Time) Item {
	if now.IsZero() {
		now = time.Now()
	}
	item.Status = StatusContradicted
	otherID = strings.TrimSpace(otherID)
	if otherID != "" {
		item.Contradicts = mergeTags(item.Contradicts, []string{otherID})
	}
	item.UpdatedAt = now
	return item
}

// MarkExpired stamps StatusExpired (and ExpiresAt when unset).
func MarkExpired(item Item, now time.Time) Item {
	if now.IsZero() {
		now = time.Now()
	}
	item.Status = StatusExpired
	if item.ExpiresAt.IsZero() {
		item.ExpiresAt = now
	}
	item.UpdatedAt = now
	return item
}

// shouldSupersede reports whether candidate should replace existing rather than
// merge. Used for preference/constraint pairs that share a topic but state a
// different durable rule — the newer statement wins and the older is marked
// superseded (still on disk for audit).
func shouldSupersede(existing Item, candidate Item) bool {
	if !existing.IsActive(time.Time{}) {
		return false
	}
	if existing.ID != "" && candidate.ID != "" && existing.ID == candidate.ID {
		return false
	}
	// Explicit organizer/tool hint: candidate.Supersedes names this existing id
	// or shares a topic token with it.
	if hintMatchesExisting(candidate.Supersedes, existing) {
		switch candidate.Kind {
		case KindPreference, KindConstraint, KindFact, KindWorkflow:
			return normalizeText(existing.Text) != normalizeText(candidate.Text)
		}
	}
	// Only durable policy-like kinds auto-supersede without an explicit hint.
	switch candidate.Kind {
	case KindPreference, KindConstraint:
	default:
		return false
	}
	if existing.Kind != "" && candidate.Kind != "" && existing.Kind != candidate.Kind {
		return false
	}
	if existing.Scope != "" && candidate.Scope != "" && existing.Scope != candidate.Scope {
		return false
	}
	if normalizeText(existing.Text) == normalizeText(candidate.Text) {
		return false // identical → merge path
	}
	// Near-duplicates (high lexical overlap) still merge: they are rewrites of
	// the same fact, not a policy change.
	overlap := tokenOverlap(existing.Text, candidate.Text)
	if overlap >= 0.55 {
		return false
	}
	shared := sharedTokenCount(existing.Text, candidate.Text)
	// Same topic, different statement: enough shared tokens to be related, but
	// not so many that the texts are essentially the same sentence.
	if shared >= 2 && overlap >= 0.12 && overlap < 0.55 {
		return true
	}
	// Stronger semantic path: opposing polarity on a shared topic.
	return detectSemanticConflict(existing, candidate)
}

// shouldContradict reports an unresolved conflict that should not silently
// merge: same topic, opposing polarity, neither side is a clear supersede.
func shouldContradict(existing Item, candidate Item) bool {
	if !existing.IsActive(time.Time{}) {
		return false
	}
	if existing.ID != "" && candidate.ID != "" && existing.ID == candidate.ID {
		return false
	}
	if normalizeText(existing.Text) == normalizeText(candidate.Text) {
		return false
	}
	// Prefer supersede for preference/constraint policy updates.
	if shouldSupersede(existing, candidate) {
		return false
	}
	return detectSemanticConflict(existing, candidate)
}

// detectSemanticConflict looks for shared topical anchors plus opposing
// polarity markers (negation / always-vs-never / enable-vs-disable). This is
// still heuristic — not an LLM judge — but stronger than pure token overlap.
func detectSemanticConflict(a, b Item) bool {
	if a.Kind != "" && b.Kind != "" && a.Kind != b.Kind {
		// Cross-kind conflicts are rare; only consider same-kind statements.
		return false
	}
	if a.Scope != "" && b.Scope != "" && a.Scope != b.Scope {
		return false
	}
	shared := sharedContentTokens(a.Text, b.Text)
	if len(shared) < 2 {
		return false
	}
	pa := polaritySignature(a.Text)
	pb := polaritySignature(b.Text)
	if pa == polarityNeutral || pb == polarityNeutral {
		return false
	}
	return pa != pb
}

type polarity int

const (
	polarityNeutral polarity = iota
	polarityAffirm
	polarityNegate
)

var affirmMarkers = []string{
	"always", "must", "prefer", "prefers", "preferred", "enable", "enabled",
	"required", "require", "use", "uses", "allow", "allowed", "yes", "true",
	"should", "keep", "默认", "总是", "必须", "优先", "启用", "允许",
}

var negateMarkers = []string{
	"never", "no", "not", "don't", "dont", "do not", "disable", "disabled",
	"avoid", "forbid", "forbidden", "disallow", "without", "false",
	"禁止", "不要", "从不", "避免", "禁用", "无需", "不必",
}

func polaritySignature(text string) polarity {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return polarityNeutral
	}
	// Strong negators scope over later affirm verbs ("never enable", "do not use").
	// Count them first so mixed sentences do not cancel to neutral.
	strongNegate := []string{
		"never", "don't", "dont", "do not", "forbid", "forbidden", "disallow",
		"禁止", "不要", "从不", "禁用",
	}
	for _, marker := range strongNegate {
		if containsWordOrPhrase(lower, marker) {
			return polarityNegate
		}
	}
	aff, neg := 0, 0
	for _, marker := range affirmMarkers {
		if containsWordOrPhrase(lower, marker) {
			aff++
		}
	}
	for _, marker := range negateMarkers {
		if containsWordOrPhrase(lower, marker) {
			neg++
		}
	}
	switch {
	case neg > aff:
		return polarityNegate
	case aff > neg:
		return polarityAffirm
	default:
		return polarityNeutral
	}
}

func containsWordOrPhrase(lower, marker string) bool {
	marker = strings.ToLower(strings.TrimSpace(marker))
	if marker == "" {
		return false
	}
	if strings.Contains(marker, " ") {
		return strings.Contains(lower, marker)
	}
	// Word-ish boundary check so "not" does not match "note".
	idx := 0
	for {
		i := strings.Index(lower[idx:], marker)
		if i < 0 {
			return false
		}
		i += idx
		beforeOK := i == 0 || !isTokenChar(rune(lower[i-1]))
		after := i + len(marker)
		afterOK := after >= len(lower) || !isTokenChar(rune(lower[after]))
		if beforeOK && afterOK {
			return true
		}
		idx = i + len(marker)
		if idx >= len(lower) {
			return false
		}
	}
}

func isTokenChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// sharedContentTokens drops ultra-common glue words so polarity conflicts need
// a real shared topic (build, tests, plan-mode, …).
func sharedContentTokens(a, b string) []string {
	stop := map[string]bool{
		"a": true, "an": true, "the": true, "and": true, "or": true, "to": true,
		"of": true, "in": true, "on": true, "for": true, "with": true, "is": true,
		"are": true, "be": true, "this": true, "that": true, "it": true, "as": true,
		"at": true, "by": true, "from": true, "user": true, "project": true,
	}
	setA := map[string]bool{}
	for _, term := range queryTerms(a) {
		if stop[term] || len(term) < 3 {
			continue
		}
		setA[term] = true
	}
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, term := range queryTerms(b) {
		if !setA[term] || seen[term] || stop[term] || len(term) < 3 {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	return out
}

func hintMatchesExisting(hint string, existing Item) bool {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return false
	}
	if existing.ID != "" && (hint == existing.ID || strings.EqualFold(hint, existing.ID)) {
		return true
	}
	// Topic-key style hints: any content token of the hint appears in existing text/tags.
	lowerText := strings.ToLower(existing.Text + " " + strings.Join(existing.Tags, " "))
	for _, term := range queryTerms(hint) {
		if len(term) < 3 {
			continue
		}
		if strings.Contains(lowerText, term) {
			return true
		}
	}
	return false
}

// applyGovernanceOnWrite resolves supersede / contradict relationships for a
// newly saved active candidate against a working set. Returns the candidate
// (possibly version-bumped) and any existing items that must be rewritten.
//
// When judge is nil, HeuristicConflictJudge is used. JevConflictJudge may be
// passed to adjudicate ambiguous same-topic pairs via System One.
func applyGovernanceOnWrite(existingItems []Item, candidate Item, now time.Time) (Item, []Item) {
	return applyGovernanceOnWriteWithJudge(context.Background(), nil, existingItems, candidate, now)
}

func applyGovernanceOnWriteWithJudge(ctx context.Context, judge ConflictJudge, existingItems []Item, candidate Item, now time.Time) (Item, []Item) {
	if now.IsZero() {
		now = time.Now()
	}
	if candidate.Status == "" {
		candidate.Status = StatusActive
	}
	if candidate.Version <= 0 {
		candidate.Version = 1
	}
	// Organizer may emit status=superseded for the *new* candidate when it is
	// describing an already-replaced fact. That should not enter retrieval as
	// active knowledge.
	if candidate.Status == StatusSuperseded || candidate.Status == StatusExpired || candidate.Status == StatusContradicted {
		return candidate, nil
	}
	if judge == nil {
		judge = HeuristicConflictJudge{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var updates []Item
	for _, existing := range existingItems {
		if existing.ID == "" || existing.ID == candidate.ID {
			continue
		}
		verdict := judge.Adjudicate(ctx, existing, candidate)
		switch verdict {
		case ConflictSame:
			// Merge is handled by the caller before governance; skip edges.
			continue
		case ConflictSupersede:
			if existing.Version >= candidate.Version {
				candidate.Version = existing.Version + 1
			}
			if candidate.Supersedes == "" {
				candidate.Supersedes = existing.ID
			} else if !containsCSVToken(candidate.Supersedes, existing.ID) {
				candidate.Supersedes = candidate.Supersedes + "," + existing.ID
			}
			topic := candidate.Topic
			if topic == "" {
				topic = existing.Topic
			}
			candidate = AddRelation(candidate, RelSupersedes, existing.ID, topic)
			marked := MarkSuperseded(existing, candidate.ID, now)
			if strings.TrimSpace(marked.Topic) == "" {
				marked.Topic = topic
			}
			if strings.TrimSpace(candidate.Topic) == "" {
				candidate.Topic = topic
			}
			updates = append(updates, marked)
		case ConflictContradict:
			topic := candidate.Topic
			if topic == "" {
				topic = existing.Topic
			}
			marked := MarkContradicted(existing, candidate.ID, now)
			marked = AddRelation(marked, RelContradicts, candidate.ID, topic)
			if strings.TrimSpace(marked.Topic) == "" {
				marked.Topic = topic
			}
			updates = append(updates, marked)
			candidate.Contradicts = mergeTags(candidate.Contradicts, []string{existing.ID})
			candidate = AddRelation(candidate, RelContradicts, existing.ID, topic)
			candidate.Status = StatusActive
			if strings.TrimSpace(candidate.Topic) == "" {
				candidate.Topic = topic
			}
		}
	}
	// Same-topic / supports / derived_from edges for the new candidate.
	candidate = buildTopicLinks(candidate, existingItems)
	return candidate, updates
}

// GC removes or marks expired / long-superseded archival entries.
//
// Zero-value options delete expired entries and keep superseded/contradicted
// forever (audit). Set DeleteSupersededAfter to eventually hard-delete those.
func (m *Manager) GC(ctx context.Context, opts GCOptions) (GCStats, error) {
	var stats GCStats
	if m == nil || m.Store == nil {
		return stats, nil
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	items, err := m.Store.List(ctx)
	if err != nil {
		return stats, err
	}
	stats.Scanned = len(items)
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		status := item.normalizedStatus()
		// Promote past-ExpiresAt active items to expired status first.
		if status == StatusActive && !item.ExpiresAt.IsZero() && !opts.Now.Before(item.ExpiresAt) {
			item = MarkExpired(item, opts.Now)
			if _, err := m.Store.Save(ctx, item); err != nil {
				return stats, err
			}
			stats.MarkedExpired++
			status = StatusExpired
		}
		switch status {
		case StatusExpired:
			if !opts.KeepExpired {
				if err := m.Store.Delete(ctx, item.ID); err != nil {
					return stats, err
				}
				m.dropVector(ctx, item.ID)
				stats.Deleted++
				stats.Expired++
			}
		case StatusSuperseded, StatusContradicted:
			if opts.DeleteSupersededAfter > 0 {
				ref := item.UpdatedAt
				if ref.IsZero() {
					ref = item.CreatedAt
				}
				if !ref.IsZero() && opts.Now.Sub(ref) >= opts.DeleteSupersededAfter {
					if err := m.Store.Delete(ctx, item.ID); err != nil {
						return stats, err
					}
					m.dropVector(ctx, item.ID)
					stats.Deleted++
					stats.Superseded++
				}
			}
		}
	}
	return stats, nil
}

// dropVector best-effort removes a vector index entry when the store supports it.
func (m *Manager) dropVector(ctx context.Context, id string) {
	if m == nil || m.Vectors == nil || strings.TrimSpace(id) == "" {
		return
	}
	type vectorDeleter interface {
		Delete(ctx context.Context, id string) error
	}
	if d, ok := m.Vectors.(vectorDeleter); ok {
		_ = d.Delete(ctx, id)
	}
}

func containsCSVToken(list, want string) bool {
	want = strings.TrimSpace(want)
	for _, item := range strings.Split(list, ",") {
		if strings.TrimSpace(item) == want {
			return true
		}
	}
	return false
}
