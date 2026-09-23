package memory

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/philippgille/chromem-go"
	"github.com/solosw/solcode/internal/embedding"
	"github.com/solosw/solcode/internal/systemone"
)

const (
	// vectorCandidateCap bounds how many chromem hits we merge into lexical results.
	vectorCandidateCap = 12
	// jevRankCandidateCap keeps Rank prompts small and cheap.
	jevRankCandidateCap = 8
)

// VectorIndex is the optional chromem-backed memory index.
type VectorIndex interface {
	Add(ctx context.Context, id, content string, metadata map[string]string, embedding []float32) error
	Query(ctx context.Context, text string, n int, where map[string]string) ([]chromem.Result, error)
	Provider() embedding.Provider
}

// embeddingStore adapts *embedding.Store to VectorIndex.
type embeddingStore struct {
	inner *embedding.Store
}

func (s embeddingStore) Add(ctx context.Context, id, content string, metadata map[string]string, vec []float32) error {
	if s.inner == nil {
		return fmt.Errorf("embedding store is nil")
	}
	return s.inner.Add(ctx, id, content, metadata, vec)
}

func (s embeddingStore) Query(ctx context.Context, text string, n int, where map[string]string) ([]chromem.Result, error) {
	if s.inner == nil {
		return nil, fmt.Errorf("embedding store is nil")
	}
	return s.inner.Query(ctx, text, n, where)
}

func (s embeddingStore) Provider() embedding.Provider {
	if s.inner == nil {
		return nil
	}
	return s.inner.Provider()
}

// WithVectorIndex attaches a chromem store for write-side indexing and retrieve merge.
func (m *Manager) WithVectorIndex(store *embedding.Store) *Manager {
	if m == nil {
		return nil
	}
	if store == nil {
		m.Vectors = nil
		return m
	}
	m.Vectors = embeddingStore{inner: store}
	return m
}

// WithDecider attaches a Jev Decider used to Rank merged retrieval candidates.
func (m *Manager) WithDecider(decider *systemone.Decider) *Manager {
	if m == nil {
		return nil
	}
	m.Decider = decider
	return m
}

func (m *Manager) indexMemory(ctx context.Context, item Item) {
	if m == nil || m.Vectors == nil {
		return
	}
	content := embedContent(item)
	if strings.TrimSpace(content) == "" || strings.TrimSpace(item.ID) == "" {
		return
	}
	meta := embedMetadata(item)
	var vec []float32
	if p := m.Vectors.Provider(); p != nil {
		if emb, err := embedding.EmbedDocument(ctx, p, content); err == nil {
			vec = emb
		}
	}
	_ = m.Vectors.Add(ctx, item.ID, content, meta, vec)
}

func embedContent(item Item) string {
	text := strings.TrimSpace(item.Text)
	if text == "" {
		return ""
	}
	var b strings.Builder
	if kind := strings.TrimSpace(string(item.Kind)); kind != "" {
		b.WriteByte('[')
		b.WriteString(kind)
		b.WriteString("] ")
	}
	b.WriteString(text)
	for _, tag := range item.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		b.WriteString(" #")
		b.WriteString(strings.TrimPrefix(tag, "#"))
	}
	return b.String()
}

func embedMetadata(item Item) map[string]string {
	meta := map[string]string{}
	if tier := strings.TrimSpace(string(item.Tier)); tier != "" {
		meta["tier"] = tier
	}
	if kind := strings.TrimSpace(string(item.Kind)); kind != "" {
		meta["kind"] = kind
	}
	if scope := strings.TrimSpace(string(item.Scope)); scope != "" {
		meta["scope"] = scope
	}
	if sid := strings.TrimSpace(item.SourceSessionID); sid != "" {
		meta["source_session_id"] = sid
	}
	if item.SourceTurn != 0 {
		meta["source_turn"] = strconv.Itoa(item.SourceTurn)
	}
	if len(item.Tags) > 0 {
		tags := make([]string, 0, len(item.Tags))
		for _, tag := range item.Tags {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				tags = append(tags, tag)
			}
		}
		if len(tags) > 0 {
			meta["tags"] = strings.Join(tags, ",")
		}
	}
	return meta
}

func (m *Manager) mergeVectorCandidates(ctx context.Context, query string, sessionID string, allowCrossSession bool, items []Item, lexical []Item, limit int) []Item {
	if m == nil || m.Vectors == nil || strings.TrimSpace(query) == "" {
		return lexical
	}
	n := vectorCandidateCap
	if limit > 0 && limit*2 > n {
		n = limit * 2
	}
	res, err := m.Vectors.Query(ctx, query, n, nil)
	if err != nil || len(res) == 0 {
		return lexical
	}
	byID := make(map[string]Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	seen := itemIDSet(lexical)
	out := append([]Item(nil), lexical...)
	for _, hit := range res {
		id := strings.TrimSpace(hit.ID)
		if id == "" || seen[id] {
			continue
		}
		item, ok := byID[id]
		if !ok {
			continue
		}
		if item.Tier == TierSensory {
			continue
		}
		if item.Tier == TierWorking && item.SourceSessionID != "" && item.SourceSessionID != sessionID {
			continue
		}
		if !allowCrossSession && item.SourceSessionID != "" && item.SourceSessionID != sessionID {
			continue
		}
		out = append(out, item)
		seen[id] = true
	}
	return out
}

func (m *Manager) rankWithJev(ctx context.Context, query string, candidates []Item, limit int) []Item {
	if m == nil || m.Decider == nil || !m.Decider.Enabled() || len(candidates) <= 1 {
		if limit > 0 && len(candidates) > limit {
			return candidates[:limit]
		}
		return candidates
	}
	pool := candidates
	if len(pool) > jevRankCandidateCap {
		pool = pool[:jevRankCandidateCap]
	}
	sysCandidates := make([]systemone.Candidate, 0, len(pool))
	byName := make(map[string]Item, len(pool))
	for _, item := range pool {
		name := strings.TrimSpace(item.ID)
		if name == "" {
			continue
		}
		desc := embedContent(item)
		if desc == "" {
			desc = strings.TrimSpace(item.Text)
		}
		sysCandidates = append(sysCandidates, systemone.Candidate{
			Name:        name,
			Description: desc,
		})
		byName[name] = item
	}
	if len(sysCandidates) == 0 {
		if limit > 0 && len(candidates) > limit {
			return candidates[:limit]
		}
		return candidates
	}
	topN := limit
	if topN <= 0 || topN > len(sysCandidates) {
		topN = len(sysCandidates)
	}
	ranked := m.Decider.Rank(ctx, map[string]any{"query": query},
		"Which remembered facts are most useful for answering what `state.query` asks? Prefer concrete durable preferences, constraints, and verified facts over scratch notes.",
		sysCandidates, topN)
	if len(ranked) == 0 {
		if limit > 0 && len(candidates) > limit {
			return candidates[:limit]
		}
		return candidates
	}
	out := make([]Item, 0, len(ranked))
	seen := map[string]bool{}
	for _, r := range ranked {
		item, ok := byName[r.Name]
		if !ok || seen[r.Name] {
			continue
		}
		out = append(out, item)
		seen[r.Name] = true
	}
	for _, item := range candidates {
		if seen[item.ID] {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, item)
		seen[item.ID] = true
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
