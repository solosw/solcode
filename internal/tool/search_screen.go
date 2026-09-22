package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/solosw/solcode/internal/systemone"
)

const (
	// maxScreenedSearchHits bounds how many hits one Jev screening request may
	// contain. Each hit is one Noul question.
	maxScreenedSearchHits  = 24
	defaultSearchScreenMin = 0.5
)

// searchResultScreener filters hits for relevance to the search query.
type searchResultScreener interface {
	Screen(ctx context.Context, query string, hits []SearchHit) []SearchHit
}

// jevSearchScreener uses one Noul per hit, with the search query as state.
type jevSearchScreener struct {
	decider        *systemone.Decider
	minProbability float64
}

func newJevSearchScreener(decider *systemone.Decider, minProbability float64) searchResultScreener {
	if decider == nil || !decider.Enabled() {
		return nil
	}
	if minProbability <= 0 || minProbability > 1 {
		minProbability = defaultSearchScreenMin
	}
	return &jevSearchScreener{decider: decider, minProbability: minProbability}
}

func (s *jevSearchScreener) Screen(ctx context.Context, query string, hits []SearchHit) []SearchHit {
	query = strings.TrimSpace(query)
	if s == nil || s.decider == nil || !s.decider.Enabled() || query == "" || len(hits) == 0 {
		return hits
	}
	candidates := make([]systemone.Candidate, 0, len(hits))
	byName := make(map[string]SearchHit, len(hits))
	for i, hit := range hits {
		if i >= maxScreenedSearchHits {
			break
		}
		name := searchHitCandidateName(i, hit)
		byName[name] = hit
		candidates = append(candidates, systemone.Candidate{
			Name:        name,
			Description: searchHitDescription(hit),
		})
	}
	if len(candidates) == 0 {
		return hits
	}

	ranked := s.decider.Screen(ctx, query,
		"Is this search result relevant to the search query in `state`?",
		candidates, s.minProbability, 0)
	if len(ranked) == 0 {
		// Fail open: keep the original ranking when Jev has no confident keepers,
		// or when the call failed. An empty screen must not erase useful results.
		return hits
	}

	out := make([]SearchHit, 0, len(ranked))
	seen := make(map[string]bool, len(ranked))
	for _, item := range ranked {
		hit, ok := byName[item.Name]
		if !ok || seen[hit.URL] {
			continue
		}
		seen[hit.URL] = true
		out = append(out, hit)
	}
	if len(out) == 0 {
		return hits
	}
	return out
}

func searchHitCandidateName(index int, hit SearchHit) string {
	title := strings.TrimSpace(hit.Title)
	if title == "" {
		title = strings.TrimSpace(hit.URL)
	}
	if title == "" {
		title = "result"
	}
	return fmt.Sprintf("%d:%s", index+1, truncateRunes(title, 80))
}

func searchHitDescription(hit SearchHit) string {
	parts := make([]string, 0, 3)
	if url := strings.TrimSpace(hit.URL); url != "" {
		parts = append(parts, "URL: "+url)
	}
	if title := strings.TrimSpace(hit.Title); title != "" {
		parts = append(parts, "Title: "+title)
	}
	if snippet := strings.TrimSpace(hit.Snippet); snippet != "" {
		parts = append(parts, "Snippet: "+truncateRunes(snippet, 240))
	}
	return strings.Join(parts, " | ")
}
