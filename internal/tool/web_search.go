package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	websearch "github.com/proiceremo/websearch"
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

func (defaultWebSearcher) Search(ctx context.Context, query string, opts websearch.SearchOptions) ([]websearch.SearchResult, error) {
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
	timeout         time.Duration
	fallbackTimeout time.Duration
}

func NewWebSearchTool() Tool {
	return newWebSearchTool(defaultWebSearcher{}, newBaiduJSONSearcher(nil))
}

func newWebSearchTool(searcher webSearcher, fallback baiduSearcher) Tool {
	if searcher == nil {
		searcher = defaultWebSearcher{}
	}
	if fallback == nil {
		fallback = newBaiduJSONSearcher(nil)
	}
	return &webSearchTool{
		searcher:        searcher,
		baiduFallback:   fallback,
		timeout:         30 * time.Second,
		fallbackTimeout: 10 * time.Second,
	}
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
	results, err := t.searcher.Search(searchCtx, params.Query, websearch.SearchOptions{
		Category:   category,
		Backend:    "auto",
		MaxResults: maxResults,
		Timeout:    int(primaryTimeout.Seconds()),
	})
	cancel()

	var hits []SearchHit
	if err != nil {
		if category == websearch.CategoryText && isTimeoutError(err) && t.baiduFallback != nil {
			fallbackTimeout := t.fallbackTimeout
			if fallbackTimeout <= 0 {
				fallbackTimeout = 10 * time.Second
			}
			fallbackCtx, fallbackCancel := context.WithTimeout(ctx, fallbackTimeout)
			hits, err = t.baiduFallback.Search(fallbackCtx, params.Query, maxResults)
			fallbackCancel()
			if err != nil {
				return ErrorResult("web search timed out and Baidu fallback failed: " + err.Error()), nil
			}
		} else {
			return ErrorResult("web search failed: " + err.Error()), nil
		}
	} else {
		hits = normalizeSearchResults(results, maxResults)
	}
	if len(hits) == 0 {
		return Result("No results found."), nil
	}

	var b strings.Builder
	for i, hit := range hits {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, hit.Title, hit.URL, hit.Snippet)
	}
	b.WriteString("\nREMINDER: Include a relevant Sources: section with markdown links in your final answer.")
	return Result(b.String()), nil
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
