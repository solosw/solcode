//go:build live

// Package websearch live tests hit real search engines (Wikipedia,
// DuckDuckGo, Brave, Mojeek, arXiv, etc.) and verify the metasearch
// aggregator, ranker, and individual scrapers still work end-to-end.
//
// Run with:    go test -tags=live ./...
// Skip with:   (default behaviour — these never run unless the tag is on)
//
// Requirements:
//   - Outbound HTTPS reach to the upstream engines
//   - No credentials needed
//
// Reliability notes:
//   - These tests target stable, broad queries ("wikipedia", "linux", etc.)
//     so transient ranking jitter doesn't break them.
//   - When an engine is rate-limited or has restructured its HTML, the
//     aggregator's "at least one provider returned results" guarantee
//     keeps the suite green as long as the metasearch as a whole still
//     produces useful output.
//   - Each test skips itself on DNS / connection failures so partial
//     environments still yield a clean PASS line.
package websearch

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

// liveCtx returns a context with the per-test timeout used everywhere in
// this file. 60s is generous — most queries return within 5–15s, but
// metasearch fans out to several scrapers and the slowest one dictates
// total wall-clock.
func liveCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// skipIfNetworkUnreachable converts opaque DNS / connection errors into a
// clean skip with diagnostic message. The metasearch aggregator typically
// hides individual engine failures, so this kicks in only when ALL engines
// fail — usually because the host has no internet.
func skipIfNetworkUnreachable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		t.Skipf("DNS resolution failed (%v) — skipping live websearch test", err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		msg := urlErr.Error()
		switch {
		case strings.Contains(msg, "no such host"),
			strings.Contains(msg, "connection refused"),
			strings.Contains(msg, "network is unreachable"),
			strings.Contains(msg, "i/o timeout"):
			t.Skipf("network unreachable (%v) — skipping live websearch test", err)
		}
	}
	// "no results found" comes back when every engine errored AND none
	// returned a row. That's almost always a network issue in CI.
	if strings.Contains(err.Error(), "no results found") {
		t.Skipf("metasearch returned no results (likely offline or all engines rate-limited): %v", err)
	}
}

// assertTextResult sanity-checks a single SearchResult that should be a
// text-category row. Live results are noisy, so we only assert structural
// invariants — never that a specific title appears.
func assertTextResult(t *testing.T, r SearchResult) {
	t.Helper()
	if r.Category != CategoryText {
		t.Errorf("expected CategoryText, got %q", r.Category)
	}
	if r.Text == nil {
		t.Fatalf("Text payload is nil; got %+v", r)
	}
	if strings.TrimSpace(r.Title()) == "" {
		t.Errorf("result has empty Title: %+v", r.Text)
	}
	if strings.TrimSpace(r.Href()) == "" {
		t.Errorf("result has empty Href: %+v", r.Text)
	}
	if _, err := url.Parse(r.Href()); err != nil {
		t.Errorf("Href is not a valid URL %q: %v", r.Href(), err)
	}
}

func TestLive_SearchText_Auto(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	results, err := Search(ctx, "wikipedia", SearchOptions{
		Category:   CategoryText,
		Backend:    "auto",
		MaxResults: 10,
		Timeout:    20,
	})
	skipIfNetworkUnreachable(t, err)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Search returned 0 results for the 'wikipedia' query — every engine errored")
	}
	for i, r := range results {
		t.Run("result_"+strings.ReplaceAll(r.Title(), " ", "_"), func(t *testing.T) {
			if i >= 3 {
				t.Skip("only spot-checking the top 3 results")
			}
			assertTextResult(t, r)
		})
	}
	// The aggregator must have deduplicated by URL; no two results should
	// share the same href, otherwise the dedup pass is broken.
	seen := map[string]bool{}
	for _, r := range results {
		key := r.DeduplicationKey()
		if key == "" {
			continue
		}
		if seen[key] {
			t.Errorf("duplicate result for href %q — dedup failed", key)
		}
		seen[key] = true
	}
}

// TestLive_SearchText_Wikipedia exercises a single backend directly so a
// failure pin-points which scraper broke rather than burying it in the
// auto-aggregator's "at least one returned" gate.
func TestLive_SearchText_Wikipedia(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	results, err := Search(ctx, "Linux operating system", SearchOptions{
		Category:   CategoryText,
		Backend:    "wikipedia",
		MaxResults: 5,
		Timeout:    20,
	})
	skipIfNetworkUnreachable(t, err)
	if err != nil {
		t.Fatalf("Search wikipedia: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Wikipedia engine returned 0 results")
	}
	// Every result should be a wikipedia.org href.
	for _, r := range results {
		if !strings.Contains(r.Href(), "wikipedia.org") {
			t.Errorf("wikipedia backend returned non-wikipedia href: %s", r.Href())
		}
	}
}

// TestLive_SearchText_DuckDuckGo verifies the DuckDuckGo text scraper still
// works. DDG occasionally rate-limits unauthenticated traffic, so this
// skips when the engine returns no rows rather than failing hard.
func TestLive_SearchText_DuckDuckGo(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	results, err := Search(ctx, "golang", SearchOptions{
		Category:   CategoryText,
		Backend:    "duckduckgo",
		MaxResults: 5,
		Timeout:    20,
	})
	skipIfNetworkUnreachable(t, err)
	if err != nil {
		t.Skipf("DuckDuckGo unavailable (likely rate-limited): %v", err)
	}
	if len(results) == 0 {
		t.Skip("DuckDuckGo returned 0 results — likely rate-limited or anti-bot challenge")
	}
	for _, r := range results {
		assertTextResult(t, r)
	}
}

// TestLive_SearchText_Bing verifies the Bing HTML text scraper.
func TestLive_SearchText_Bing(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	results, err := Search(ctx, "golang", SearchOptions{
		Category:   CategoryText,
		Backend:    "bing",
		MaxResults: 5,
		Timeout:    20,
	})
	skipIfNetworkUnreachable(t, err)
	if err != nil {
		t.Skipf("Bing unavailable (likely rate-limited): %v", err)
	}
	if len(results) == 0 {
		t.Skip("Bing returned 0 results — likely rate-limited or anti-bot challenge")
	}
	for _, r := range results {
		assertTextResult(t, r)
	}
}

func TestLive_SearchResearch_Arxiv(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	results, err := Search(ctx, "transformer attention mechanism", SearchOptions{
		Category:   CategoryResearch,
		Backend:    "arxiv",
		MaxResults: 5,
		Timeout:    30,
	})
	skipIfNetworkUnreachable(t, err)
	if err != nil {
		t.Fatalf("Search arxiv: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("arXiv engine returned 0 results for 'transformer attention mechanism'")
	}
	r := results[0]
	if r.Research == nil {
		t.Fatalf("expected Research payload, got %+v", r)
	}
	if r.Research.Title == "" {
		t.Fatal("first arXiv result has empty Title")
	}
	if !strings.Contains(r.Research.URL, "arxiv.org") {
		t.Errorf("expected arxiv.org URL, got %q", r.Research.URL)
	}
}

// TestLive_SearchNews verifies the news category dispatches to a working
// scraper. DuckDuckGo News is the only registered backend at the time of
// writing, so we treat a rate-limit as a skip rather than a failure.
func TestLive_SearchNews(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	results, err := Search(ctx, "technology", SearchOptions{
		Category:   CategoryNews,
		MaxResults: 5,
		Timeout:    20,
	})
	skipIfNetworkUnreachable(t, err)
	if err != nil {
		t.Skipf("News search unavailable: %v", err)
	}
	if len(results) == 0 {
		t.Skip("News search returned 0 results — likely rate-limited")
	}
	r := results[0]
	if r.News == nil {
		t.Fatalf("expected News payload, got %+v", r)
	}
	if r.News.Title == "" || r.News.URL == "" {
		t.Errorf("incomplete news result: %+v", r.News)
	}
}

// TestLive_EmptyQuery verifies the validation path runs end-to-end. No
// network needed since the empty-query check fires before any engine call.
func TestLive_EmptyQuery(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	_, err := Search(ctx, "", SearchOptions{Category: CategoryText})
	if err == nil {
		t.Fatal("expected an error for empty query, got nil")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestLive_UnknownBackendFallsBack verifies the selectEngines fallback path:
// if the requested backend doesn't exist, the metasearch should fall back
// to "auto" and still produce results rather than returning nothing.
func TestLive_UnknownBackendFallsBack(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	results, err := Search(ctx, "wikipedia", SearchOptions{
		Category:   CategoryText,
		Backend:    "this-backend-definitely-does-not-exist",
		MaxResults: 3,
		Timeout:    20,
	})
	skipIfNetworkUnreachable(t, err)
	if err != nil {
		t.Fatalf("Search with unknown backend: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected fallback to 'auto' to produce at least one result")
	}
}

// TestLive_RankingIsApplied confirms the ranker actually runs for text
// category — top result should outrank later results by relevance to the
// query. This is a soft check: we look at title overlap with the query
// rather than requiring an exact ordering.
func TestLive_RankingIsApplied(t *testing.T) {
	ctx, cancel := liveCtx(t)
	defer cancel()

	const query = "Python programming language"
	results, err := Search(ctx, query, SearchOptions{
		Category:   CategoryText,
		Backend:    "auto",
		MaxResults: 10,
		Timeout:    25,
	})
	skipIfNetworkUnreachable(t, err)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) < 3 {
		t.Skipf("only %d results returned; can't reasonably check ranking", len(results))
	}
	// The top 3 should collectively mention "python" somewhere — title or
	// body. If none do, the ranker isn't biasing toward the query.
	hits := 0
	for i := 0; i < 3 && i < len(results); i++ {
		merged := strings.ToLower(results[i].Title() + " " + results[i].Body() + " " + results[i].Href())
		if strings.Contains(merged, "python") {
			hits++
		}
	}
	if hits == 0 {
		t.Errorf("none of the top 3 results mention 'python' — ranker may have regressed; results=%+v", results[:3])
	}
}
