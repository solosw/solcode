package memory

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
)

// RelationType is a typed edge between archival memory items.
//
// This is a lightweight adjacency model stored on Item itself (not a separate
// graph DB): supersede/contradict already existed; the rest fill common
// "memory net" needs without a full KG engine.
type RelationType string

const (
	RelSupersedes  RelationType = "supersedes"
	RelContradicts RelationType = "contradicts"
	RelSupports    RelationType = "supports"
	RelDependsOn   RelationType = "depends_on"
	RelSameTopic   RelationType = "same_topic"
	RelDerivedFrom RelationType = "derived_from"
)

// Relation is one directed edge Item → OtherID.
type Relation struct {
	Type    RelationType `json:"type"`
	OtherID string       `json:"other_id"`
	// Topic is optional; filled for same_topic / supersede chains.
	Topic string `json:"topic,omitempty"`
}

// TopicBelief is the current active belief for one topic key.
type TopicBelief struct {
	Topic   string  `json:"topic"`
	Item    Item    `json:"item"`
	Version int     `json:"version"`
	History []string `json:"history,omitempty"` // older ids newest-first
}

// TopicHistory is the full chain for one topic (active + superseded).
type TopicHistory struct {
	Topic  string `json:"topic"`
	Items  []Item `json:"items"` // newest-first
	Active *Item  `json:"active,omitempty"`
}

// MemoryGraph is a read model over List()'d items: topics, edges, beliefs.
type MemoryGraph struct {
	Topics  []TopicBelief `json:"topics"`
	Edges   []GraphEdge   `json:"edges"`
	Orphans int           `json:"orphans"` // active items with empty topic
	Scanned int           `json:"scanned"`
}

const (
	// governanceScanLimit is how many archival items governance may inspect
	// beyond the tiny write working set, so far-topic conflicts still resolve.
	governanceScanLimit = 64
	// maxTopicKeyRunes bounds stored Topic keys.
	maxTopicKeyRunes = 48
)

// GraphEdge is a directed edge with both endpoints resolved for tooling.
type GraphEdge struct {
	Type   RelationType `json:"type"`
	FromID string       `json:"from_id"`
	ToID   string       `json:"to_id"`
	Topic  string       `json:"topic,omitempty"`
}

// DeriveTopicKey builds a stable short topic key from kind/scope/text/tags.
// Empty text yields "".
func DeriveTopicKey(item Item) string {
	if t := strings.TrimSpace(item.Topic); t != "" {
		return truncateTopicKey(normalizeTopicKey(t))
	}
	text := strings.TrimSpace(item.Text)
	if text == "" {
		return ""
	}
	stop := topicStopWords()
	terms := queryTerms(text)
	picked := make([]string, 0, 4)
	for _, term := range terms {
		if stop[term] || len([]rune(term)) < 3 {
			continue
		}
		picked = append(picked, term)
		if len(picked) >= 4 {
			break
		}
	}
	if len(picked) == 0 {
		// Fall back to first non-stop token even if short.
		for _, term := range terms {
			if stop[term] {
				continue
			}
			picked = append(picked, term)
			break
		}
	}
	if len(picked) == 0 {
		return ""
	}
	kind := strings.ToLower(strings.TrimSpace(string(item.Kind)))
	scope := strings.ToLower(strings.TrimSpace(string(item.Scope)))
	parts := make([]string, 0, 3)
	if kind != "" {
		parts = append(parts, kind)
	}
	if scope != "" && scope != "project" {
		parts = append(parts, scope)
	}
	parts = append(parts, strings.Join(picked, "-"))
	return truncateTopicKey(strings.Join(parts, ":"))
}

func normalizeTopicKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == ':' || r == '_' || r == '-' || r == '.' || unicode.IsSpace(r):
			if b.Len() == 0 || lastDash {
				continue
			}
			if r == ':' {
				b.WriteRune(':')
			} else {
				b.WriteRune('-')
			}
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-:")
	return out
}

func truncateTopicKey(value string) string {
	runes := []rune(value)
	if len(runes) <= maxTopicKeyRunes {
		return value
	}
	return string(runes[:maxTopicKeyRunes])
}

func topicStopWords() map[string]bool {
	return map[string]bool{
		"a": true, "an": true, "the": true, "and": true, "or": true, "to": true,
		"of": true, "in": true, "on": true, "for": true, "with": true, "is": true,
		"are": true, "be": true, "this": true, "that": true, "it": true, "as": true,
		"at": true, "by": true, "from": true, "user": true, "project": true,
		"prefer": true, "prefers": true, "preferred": true, "always": true,
		"never": true, "must": true, "should": true, "use": true, "uses": true,
		"keep": true, "when": true, "into": true, "about": true,
	}
}

// EnsureTopic sets Topic when empty using DeriveTopicKey.
func EnsureTopic(item Item) Item {
	if strings.TrimSpace(item.Topic) == "" {
		item.Topic = DeriveTopicKey(item)
	} else {
		item.Topic = truncateTopicKey(normalizeTopicKey(item.Topic))
	}
	return item
}

// AddRelation appends a typed edge if missing.
func AddRelation(item Item, relType RelationType, otherID, topic string) Item {
	otherID = strings.TrimSpace(otherID)
	if otherID == "" || relType == "" {
		return item
	}
	topic = strings.TrimSpace(topic)
	for _, rel := range item.Relations {
		if rel.Type == relType && rel.OtherID == otherID {
			return item
		}
	}
	item.Relations = append(item.Relations, Relation{
		Type:    relType,
		OtherID: otherID,
		Topic:   topic,
	})
	return item
}

// RelationsOf returns edges of the given types (empty types → all).
func RelationsOf(item Item, types ...RelationType) []Relation {
	if len(types) == 0 {
		return append([]Relation(nil), item.Relations...)
	}
	allow := map[RelationType]bool{}
	for _, t := range types {
		allow[t] = true
	}
	out := make([]Relation, 0, len(item.Relations))
	for _, rel := range item.Relations {
		if allow[rel.Type] {
			out = append(out, rel)
		}
	}
	// Also project legacy supersede/contradict fields into virtual edges.
	if allow[RelSupersedes] {
		for _, id := range splitCSV(item.Supersedes) {
			out = append(out, Relation{Type: RelSupersedes, OtherID: id, Topic: item.Topic})
		}
	}
	if allow[RelContradicts] {
		for _, id := range item.Contradicts {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			out = append(out, Relation{Type: RelContradicts, OtherID: id, Topic: item.Topic})
		}
	}
	return out
}

func splitCSV(list string) []string {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	parts := strings.Split(list, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// CollectEdges flattens all items into directed graph edges (deduped).
func CollectEdges(items []Item) []GraphEdge {
	type key struct {
		t, from, to string
	}
	seen := map[key]GraphEdge{}
	for _, item := range items {
		from := strings.TrimSpace(item.ID)
		if from == "" {
			continue
		}
		for _, rel := range RelationsOf(item) {
			to := strings.TrimSpace(rel.OtherID)
			if to == "" {
				continue
			}
			k := key{t: string(rel.Type), from: from, to: to}
			if _, ok := seen[k]; ok {
				continue
			}
			topic := rel.Topic
			if topic == "" {
				topic = item.Topic
			}
			seen[k] = GraphEdge{Type: rel.Type, FromID: from, ToID: to, Topic: topic}
		}
	}
	out := make([]GraphEdge, 0, len(seen))
	for _, edge := range seen {
		out = append(out, edge)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type == out[j].Type {
			if out[i].FromID == out[j].FromID {
				return out[i].ToID < out[j].ToID
			}
			return out[i].FromID < out[j].FromID
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// sameTopic reports whether two items share a topic key or enough content tokens.
func sameTopic(a, b Item) bool {
	ta := strings.TrimSpace(a.Topic)
	tb := strings.TrimSpace(b.Topic)
	if ta != "" && tb != "" && strings.EqualFold(ta, tb) {
		return true
	}
	if ta != "" && (hintMatchesExisting(ta, b) || strings.Contains(strings.ToLower(b.Text), strings.ToLower(ta))) {
		return true
	}
	if tb != "" && (hintMatchesExisting(tb, a) || strings.Contains(strings.ToLower(a.Text), strings.ToLower(tb))) {
		return true
	}
	shared := sharedContentTokens(a.Text, b.Text)
	return len(shared) >= 2
}

// buildTopicLinks adds same_topic / supports edges between candidate and peers.
func buildTopicLinks(candidate Item, peers []Item) Item {
	candidate = EnsureTopic(candidate)
	for _, peer := range peers {
		if peer.ID == "" || peer.ID == candidate.ID {
			continue
		}
		if !sameTopic(candidate, peer) {
			continue
		}
		topic := candidate.Topic
		if topic == "" {
			topic = peer.Topic
		}
		candidate = AddRelation(candidate, RelSameTopic, peer.ID, topic)
		// A higher-confidence active peer "supports" a related weaker fact.
		if peer.IsActive(time.Time{}) && candidate.IsActive(time.Time{}) {
			if peer.Confidence >= candidate.Confidence && peer.Confidence >= 0.75 {
				candidate = AddRelation(candidate, RelSupports, peer.ID, topic)
			}
		}
		// Derived-from-summary candidates depend on their episodic source when
		// SessionMemoryRef is set — encoded as depends_on the ref string only if
		// it looks like a memory id (mem_…). Session refs stay on SessionMemoryRef.
		if candidate.DerivedFromSummary && strings.HasPrefix(peer.ID, "mem_") &&
			normalizeText(peer.Text) != normalizeText(candidate.Text) &&
			tokenOverlap(peer.Text, candidate.Text) >= 0.35 {
			candidate = AddRelation(candidate, RelDerivedFrom, peer.ID, topic)
		}
	}
	return candidate
}

// SelectCurrentBeliefs returns one active item per topic (highest version,
// then confidence, then recency). Topics with only inactive items are omitted.
func SelectCurrentBeliefs(items []Item, limit int) []TopicBelief {
	if limit <= 0 {
		limit = 32
	}
	now := time.Now()
	byTopic := map[string][]Item{}
	for _, item := range items {
		item = EnsureTopic(item)
		if !item.IsActive(now) {
			continue
		}
		topic := strings.TrimSpace(item.Topic)
		if topic == "" {
			continue
		}
		byTopic[topic] = append(byTopic[topic], item)
	}
	beliefs := make([]TopicBelief, 0, len(byTopic))
	for topic, list := range byTopic {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Version != list[j].Version {
				return list[i].Version > list[j].Version
			}
			if list[i].Confidence != list[j].Confidence {
				return list[i].Confidence > list[j].Confidence
			}
			return list[i].UpdatedAt.After(list[j].UpdatedAt)
		})
		best := list[0]
		history := make([]string, 0, 4)
		// Pull superseded ids from the winner's supersedes list.
		for _, id := range splitCSV(best.Supersedes) {
			history = append(history, id)
		}
		beliefs = append(beliefs, TopicBelief{
			Topic:   topic,
			Item:    best,
			Version: best.Version,
			History: history,
		})
	}
	sort.SliceStable(beliefs, func(i, j int) bool {
		si := EvolvedImportance(beliefs[i].Item, now, 0.03, 0.12)
		sj := EvolvedImportance(beliefs[j].Item, now, 0.03, 0.12)
		if si == sj {
			return beliefs[i].Topic < beliefs[j].Topic
		}
		return si > sj
	})
	if len(beliefs) > limit {
		beliefs = beliefs[:limit]
	}
	return beliefs
}

// TopicHistoryOf returns newest-first items sharing topic (active + inactive).
func TopicHistoryOf(items []Item, topic string) TopicHistory {
	topic = truncateTopicKey(normalizeTopicKey(topic))
	out := TopicHistory{Topic: topic}
	if topic == "" {
		return out
	}
	now := time.Now()
	matched := make([]Item, 0)
	for _, item := range items {
		item = EnsureTopic(item)
		if !strings.EqualFold(strings.TrimSpace(item.Topic), topic) {
			// Also accept supersede-hint style membership.
			if !hintMatchesExisting(topic, item) {
				continue
			}
		}
		matched = append(matched, item)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Version != matched[j].Version {
			return matched[i].Version > matched[j].Version
		}
		return matched[i].UpdatedAt.After(matched[j].UpdatedAt)
	})
	out.Items = matched
	for i := range matched {
		if matched[i].IsActive(now) {
			item := matched[i]
			out.Active = &item
			break
		}
	}
	return out
}

// BuildMemoryGraph assembles a topic/edge summary for diagnostics and tools.
func BuildMemoryGraph(items []Item, beliefLimit int) MemoryGraph {
	beliefs := SelectCurrentBeliefs(items, beliefLimit)
	edges := CollectEdges(items)
	orphans := 0
	now := time.Now()
	for _, item := range items {
		if !item.IsActive(now) {
			continue
		}
		if strings.TrimSpace(EnsureTopic(item).Topic) == "" {
			orphans++
		}
	}
	return MemoryGraph{
		Topics:  beliefs,
		Edges:   edges,
		Orphans: orphans,
		Scanned: len(items),
	}
}

// FormatCurrentBeliefs renders a compact "current belief set" for prompts/tools.
func FormatCurrentBeliefs(beliefs []TopicBelief) string {
	if len(beliefs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Current beliefs (one per topic):\n")
	for _, belief := range beliefs {
		text := strings.TrimSpace(belief.Item.Text)
		if text == "" {
			continue
		}
		b.WriteString("- [")
		b.WriteString(belief.Topic)
		b.WriteString("] ")
		if kind := strings.TrimSpace(string(belief.Item.Kind)); kind != "" {
			b.WriteString("(")
			b.WriteString(kind)
			b.WriteString(") ")
		}
		b.WriteString(text)
		if belief.Version > 1 {
			b.WriteString(" (v")
			b.WriteString(intString(belief.Version))
			b.WriteByte(')')
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func intString(n int) string {
	if n == 0 {
		return "0"
	}
	var neg bool
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [32]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// expandGovernanceSet grows the write-time comparison set with same-topic and
// high-overlap peers from the full store list (capped).
func expandGovernanceSet(all, working []Item, candidate Item) []Item {
	candidate = EnsureTopic(candidate)
	seen := itemIDSet(working)
	out := append([]Item(nil), working...)
	add := func(item Item) {
		if item.ID == "" || item.ID == candidate.ID || seen[item.ID] {
			return
		}
		seen[item.ID] = true
		out = append(out, item)
	}
	// Pass 1: same topic key / shared content.
	for _, item := range all {
		if len(out) >= governanceScanLimit {
			break
		}
		if sameTopic(candidate, item) || shouldSupersede(item, candidate) || shouldContradict(item, candidate) {
			add(item)
		}
	}
	// Pass 2: fill with tier-relevant leftovers if still small.
	if len(out) < governanceScanLimit {
		sorted := sortByTierRelevance(all, analyzeRetrievalQuery(candidate.Text), candidate.SourceSessionID)
		for _, item := range sorted {
			if len(out) >= governanceScanLimit {
				break
			}
			add(item)
		}
	}
	return out
}

// (m *Manager) CurrentBeliefs lists topic winners from the store.
func (m *Manager) CurrentBeliefs(ctx context.Context, limit int) ([]TopicBelief, error) {
	if m == nil || m.Store == nil {
		return nil, nil
	}
	items, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	return SelectCurrentBeliefs(items, limit), nil
}

// (m *Manager) TopicHistory returns the chain for one topic key.
func (m *Manager) TopicHistory(ctx context.Context, topic string) (TopicHistory, error) {
	var empty TopicHistory
	if m == nil || m.Store == nil {
		return empty, nil
	}
	items, err := m.Store.List(ctx)
	if err != nil {
		return empty, err
	}
	return TopicHistoryOf(items, topic), nil
}

// (m *Manager) Graph returns a snapshot graph over archival memory.
func (m *Manager) Graph(ctx context.Context, beliefLimit int) (MemoryGraph, error) {
	var empty MemoryGraph
	if m == nil || m.Store == nil {
		return empty, nil
	}
	items, err := m.Store.List(ctx)
	if err != nil {
		return empty, err
	}
	return BuildMemoryGraph(items, beliefLimit), nil
}
