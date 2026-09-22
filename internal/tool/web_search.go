package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	websearch "github.com/proiceremo/websearch"
	"github.com/solosw/solcode/internal/systemone"
)

const WebSearchToolName = "WebSearch"

// WebSearchParams controls a metasearch query.
type WebSearchParams struct {
	Query      string `json:"query"`
	Category   string `json:"category,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

// SearchHit is the normalized result used in the tool response.
type SearchHit struct {
	Title   string
	URL     string
	Snippet string
}

type webSearcher interface {
	Search(ctx context.Context, query string, opts websearch.SearchOptions) ([]websearch.SearchResult, error)
}

type defaultWebSearcher struct{}

func (defaultWebSearcher) Search(ctx context.Context, query string, opts websearch.SearchOptions) (results []websearch.SearchResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			results = nil
			err = fmt.Errorf("websearch backend panicked: %v", recovered)
		}
	}()
	return websearch.Search(ctx, query, opts)
}

// baiduSearcher is a text-search fallback used only when the metasearch call
// times out. It intentionally performs one normal request; it does not try to
// bypass rate limits or CAPTCHA challenges.
type baiduSearcher interface {
	Search(ctx context.Context, query string, maxResults int) ([]SearchHit, error)
}

type webSearchTool struct {
	BaseTool
	searcher        webSearcher
	baiduFallback   baiduSearcher
	parallel        parallelSearcher
	screener        searchResultScreener
	timeout         time.Duration
	fallbackTimeout time.Duration
}

// WebSearchOptions configures optional Parallel and Jev screening backends.
type WebSearchOptions struct {
	Parallel parallelSearcher
	Screener searchResultScreener
}

func NewWebSearchTool() Tool {
	return NewWebSearchToolWithOptions(WebSearchOptions{})
}

// NewWebSearchToolWithOptions builds WebSearch with optional Parallel merge and
// Jev result screening. Parallel defaults to the keyless MCP endpoint.
func NewWebSearchToolWithOptions(opts WebSearchOptions) Tool {
	parallel := opts.Parallel
	if parallel == nil {
		parallel = newParallelMCPSearcher(nil)
	}
	return newWebSearchTool(defaultWebSearcher{}, newBaiduJSONSearcher(nil), parallel, opts.Screener)
}

func newWebSearchTool(searcher webSearcher, fallback baiduSearcher, parallel parallelSearcher, screener searchResultScreener) Tool {
	if searcher == nil {
		searcher = defaultWebSearcher{}
	}
	if fallback == nil {
		fallback = newBaiduJSONSearcher(nil)
	}
	return &webSearchTool{
		searcher:        searcher,
		baiduFallback:   fallback,
		parallel:        parallel,
		screener:        screener,
		timeout:         30 * time.Second,
		fallbackTimeout: 10 * time.Second,
	}
}

// WithSearchScreener attaches a Jev (or test) result screener.
func (t *webSearchTool) WithSearchScreener(screener searchResultScreener) *webSearchTool {
	if t == nil {
		return nil
	}
	t.screener = screener
	return t
}

func (t *webSearchTool) Name() string { return WebSearchToolName }
func (t *webSearchTool) Description() string {
	return `Search the web using the configured multi-engine metasearch backend and return structured results.
Use for recent events, documentation, or information beyond your knowledge cutoff.
Categories: text (default), images, news, videos, books, research.
After answering from results, include a "Sources:" section with relevant markdown links.`
}
func (t *webSearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query",
			},
			"category": map[string]any{
				"type":        "string",
				"enum":        []string{"text", "images", "news", "videos", "books", "research"},
				"description": "Result category (default: text)",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Maximum result count (default 10, maximum 50)",
			},
		},
		"required": []string{"query"},
	}
}
func (t *webSearchTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *webSearchTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *webSearchTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *webSearchTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	_ = uctx
	var params WebSearchParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	params.Query = strings.TrimSpace(params.Query)
	if params.Query == "" {
		return ErrorResult("query is required"), nil
	}
	category, err := webSearchCategory(params.Category)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	maxResults := params.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}

	primaryTimeout := t.timeout
	if primaryTimeout <= 0 {
		primaryTimeout = 30 * time.Second
	}
	searchCtx, cancel := context.WithTimeout(ctx, primaryTimeout)
	defer cancel()

	hits, err := t.searchMerged(searchCtx, params.Query, category, maxResults, primaryTimeout)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	if len(hits) == 0 {
		return Result("No results found."), nil
	}
	if t.screener != nil {
		hits = t.screener.Screen(ctx, params.Query, hits)
		if maxResults > 0 && len(hits) > maxResults {
			hits = hits[:maxResults]
		}
	}

	var b strings.Builder
	for i, hit := range hits {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, hit.Title, hit.URL, hit.Snippet)
	}
	b.WriteString("\nREMINDER: Include a relevant Sources: section with markdown links in your final answer.")
	return Result(b.String()), nil
}

func (t *webSearchTool) searchMerged(ctx context.Context, query string, category websearch.Category, maxResults int, primaryTimeout time.Duration) ([]SearchHit, error) {
	type outcome struct {
		hits []SearchHit
		err  error
	}

	metaCh := make(chan outcome, 1)
	go func() {
		results, err := t.searcher.Search(ctx, query, websearch.SearchOptions{
			Category:   category,
			Backend:    "auto",
			MaxResults: maxResults,
			Timeout:    int(primaryTimeout.Seconds()),
		})
		if err != nil {
			metaCh <- outcome{err: err}
			return
		}
		metaCh <- outcome{hits: normalizeSearchResults(results, maxResults)}
	}()

	var parallelHits []SearchHit
	var parallelErr error
	var wg sync.WaitGroup
	if category == websearch.CategoryText && t.parallel != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hits, err := t.parallel.Search(ctx, query, maxResults)
			if err != nil {
				parallelErr = err
				return
			}
			parallelHits = hits
		}()
	}

	meta := <-metaCh
	wg.Wait()

	var hits []SearchHit
	var primaryErr error
	if meta.err != nil {
		primaryErr = meta.err
		if category == websearch.CategoryText && shouldUseBaiduFallback(meta.err) && t.baiduFallback != nil {
			fallbackTimeout := t.fallbackTimeout
			if fallbackTimeout <= 0 {
				fallbackTimeout = 10 * time.Second
			}
			fallbackCtx, fallbackCancel := context.WithTimeout(ctx, fallbackTimeout)
			baiduHits, baiduErr := t.baiduFallback.Search(fallbackCtx, query, maxResults)
			fallbackCancel()
			if baiduErr != nil {
				primaryErr = fmt.Errorf("web search failed and Baidu fallback failed: %w", baiduErr)
			} else {
				hits = baiduHits
				primaryErr = nil
			}
		} else {
			primaryErr = fmt.Errorf("web search failed: %w", meta.err)
		}
	} else {
		hits = meta.hits
	}

	if len(parallelHits) > 0 {
		hits = mergeSearchHits(parallelHits, hits, maxResults)
		primaryErr = nil
	}
	if len(hits) == 0 && primaryErr != nil {
		if parallelErr != nil {
			return nil, fmt.Errorf("%v (parallel: %v)", primaryErr, parallelErr)
		}
		return nil, primaryErr
	}
	return hits, nil
}

// mergeSearchHits prefers Parallel hits first, then fills from the other source.
func mergeSearchHits(primary, secondary []SearchHit, limit int) []SearchHit {
	if limit <= 0 {
		return nil
	}
	out := make([]SearchHit, 0, limit)
	seen := make(map[string]bool, limit)
	appendUnique := func(items []SearchHit) {
		for _, hit := range items {
			url := strings.TrimSpace(hit.URL)
			if url == "" || seen[url] {
				continue
			}
			seen[url] = true
			title := strings.TrimSpace(hit.Title)
			if title == "" {
				title = url
			}
			out = append(out, SearchHit{
				Title:   title,
				URL:     url,
				Snippet: strings.TrimSpace(hit.Snippet),
			})
			if len(out) == limit {
				return
			}
		}
	}
	appendUnique(primary)
	if len(out) < limit {
		appendUnique(secondary)
	}
	return out
}

func webSearchCategory(raw string) (websearch.Category, error) {
	category := strings.ToLower(strings.TrimSpace(raw))
	if category == "" {
		return websearch.CategoryText, nil
	}
	switch websearch.Category(category) {
	case websearch.CategoryText,
		websearch.CategoryImages,
		websearch.CategoryNews,
		websearch.CategoryVideos,
		websearch.CategoryBooks,
		websearch.CategoryResearch:
		return websearch.Category(category), nil
	default:
		return "", fmt.Errorf("unsupported category %q; use text, images, news, videos, books, or research", raw)
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isWebSearchPanicError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "websearch backend panicked:")
}

// shouldUseBaiduFallback covers timeouts and known third-party backend panics
// (for example arXiv HTML parse nil dereferences inside proiceremo/websearch).
func shouldUseBaiduFallback(err error) bool {
	return isTimeoutError(err) || isWebSearchPanicError(err)
}

func normalizeSearchResults(results []websearch.SearchResult, limit int) []SearchHit {
	if limit <= 0 {
		return nil
	}
	out := make([]SearchHit, 0, min(limit, len(results)))
	seen := make(map[string]bool, len(results))
	for _, result := range results {
		rawURL := strings.TrimSpace(result.Href())
		if rawURL == "" || seen[rawURL] {
			continue
		}
		seen[rawURL] = true
		title := strings.TrimSpace(result.Title())
		if title == "" {
			title = rawURL
		}
		out = append(out, SearchHit{
			Title:   title,
			URL:     rawURL,
			Snippet: strings.TrimSpace(result.Body()),
		})
		if len(out) == limit {
			break
		}
	}
	return out
}

// NewWebSearchToolWithJev is a convenience for app wiring: Parallel keyless plus
// optional Jev screening when the decision layer is enabled.
func NewWebSearchToolWithJev(decider *systemone.Decider, minProbability float64) Tool {
	return NewWebSearchToolWithOptions(WebSearchOptions{
		Screener: newJevSearchScreener(decider, minProbability),
	})
}

// ConfigureWebSearchScreening attaches a Jev result screener to the registered
// WebSearch tool when Jev is enabled. No-op when the tool is missing or Jev is off.
func ConfigureWebSearchScreening(registry *Registry, decider *systemone.Decider, minProbability float64) {
	if registry == nil {
		return
	}
	found := registry.Find(WebSearchToolName)
	wt, ok := found.(*webSearchTool)
	if !ok || wt == nil {
		return
	}
	wt.WithSearchScreener(newJevSearchScreener(decider, minProbability))
}
