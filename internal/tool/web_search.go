package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// WebSearchParams is the input schema for the WebSearch tool.
type WebSearchParams struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

// SearchHit is a single web search result.
type SearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// WebSearchOutput is the structured output of a WebSearch invocation.
type WebSearchOutput struct {
	Query           string      `json:"query"`
	Results         []SearchHit `json:"results"`
	DurationSeconds float64     `json:"duration_seconds"`
}

const (
	WebSearchToolName = "WebSearch"
	DefaultMaxResults = 10
	MaxResultsCap     = 50
	SearchHTTPTimeout = 15 * time.Second
)

type webSearchTool struct {
	BaseTool
	client *http.Client
	apiKey string // optional Brave/SerpAPI key
}

var ddgHTMLSearchURL = "https://html.duckduckgo.com/html/"

// NewWebSearchTool creates a new WebSearch tool.
func NewWebSearchTool() Tool {
	return &webSearchTool{
		client: &http.Client{Timeout: SearchHTTPTimeout},
	}
}

// SetAPIKey sets an optional search API key (Brave Search or SerpAPI).
func (t *webSearchTool) SetAPIKey(key string) {
	t.apiKey = key
}

func (t *webSearchTool) Name() string                           { return WebSearchToolName }
func (t *webSearchTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *webSearchTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *webSearchTool) Description() string {
	return `Searches the web for information and returns structured results.
- Use for: recent events, documentation, anything beyond your knowledge cutoff
- Returns title, URL, and snippet for each result
- Supports domain filtering with allowed_domains / blocked_domains
- Results limited to 10 by default (max 50)

IMPORTANT: After answering based on search results, you MUST include a "Sources:" section
listing relevant URLs as markdown links. This is mandatory.`
}

func (t *webSearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Max results (default %d, max %d)", DefaultMaxResults, MaxResultsCap),
			},
			"allowed_domains": map[string]any{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Only include results from these domains",
			},
			"blocked_domains": map[string]any{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Never include results from these domains",
			},
		},
		"required": []string{"query"},
	}
}

func (t *webSearchTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params WebSearchParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if params.Query == "" {
		return ErrorResult("query is required"), nil
	}
	if len(params.Query) < 2 {
		return ErrorResult("query must be at least 2 characters"), nil
	}

	maxResults := params.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	if maxResults > MaxResultsCap {
		maxResults = MaxResultsCap
	}

	start := time.Now()
	hits, err := t.search(ctx, params.Query, maxResults, params.AllowedDomains, params.BlockedDomains)
	elapsed := time.Since(start)

	if err != nil {
		return &ContentBlock{
			Type: "text",
			Text: "Web search failed: " + err.Error(),
		}, nil
	}

	// Build readable text output for the model
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Web search results for: %q (%d results in %.1fs)\n\n", params.Query, len(hits), elapsed.Seconds()))
	for i, hit := range hits {
		sb.WriteString(fmt.Sprintf("[%d] %s\n   %s\n", i+1, hit.Title, hit.URL))
		if hit.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", hit.Snippet))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("REMINDER: Include relevant sources as markdown links in your answer.")

	return Result(sb.String()), nil
}

// search dispatches to the configured backend.
func (t *webSearchTool) search(ctx context.Context, query string, maxResults int, allowed, blocked []string) ([]SearchHit, error) {
	return searchDuckDuckGo(ctx, t.client, query, maxResults, allowed, blocked)
}

// searchDuckDuckGo scrapes DuckDuckGo HTML search results.
func searchDuckDuckGo(ctx context.Context, client *http.Client, query string, maxResults int, allowed, blocked []string) ([]SearchHit, error) {
	form := url.Values{"q": {query}}
	reqURL := ddgHTMLSearchURL

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 202 || resp.StatusCode == 403 {
			return nil, fmt.Errorf("search blocked (CAPTCHA/rate-limit, HTTP %d). "+
				"Try a different query or wait a moment", resp.StatusCode)
		}
		return nil, fmt.Errorf("search returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	hits, err := parseDDGHTML(body, maxResults, allowed, blocked)
	if err != nil {
		return nil, err
	}

	// Detect CAPTCHA
	if len(hits) == 0 && detectDDGCaptcha(body) {
		return nil, fmt.Errorf("search engine returned a CAPTCHA page (too many requests). " +
			"Try a different query or wait a moment")
	}

	return hits, nil
}

func parseDDGHTML(body []byte, maxResults int, allowed, blocked []string) ([]SearchHit, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	allowedSet := strSet(allowed)
	blockedSet := strSet(blocked)

	var hits []SearchHit
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(hits) >= maxResults {
			return
		}

		if n.Type == html.ElementNode && n.Data == "a" {
			class := getAttr(n, "class")
			if strings.Contains(class, "result__a") {
				hit := extractDDGResult(n)
				if hit.URL != "" && hit.Title != "" {
					if domainMatches(hit.URL, allowedSet, blockedSet) {
						hits = append(hits, hit)
					}
				}
				return // don't recurse into result links
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return hits, nil
}

func extractDDGResult(n *html.Node) SearchHit {
	var hit SearchHit
	var extract func(*html.Node)
	extract = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			class := getAttr(node, "class")
			if strings.Contains(class, "result__a") {
				hit.URL = getAttr(node, "href")
				hit.Title = textContent(node)
				// DDG redirect URL: extract real URL from uddg= param
				if strings.Contains(hit.URL, "uddg=") {
					if u, err := url.Parse(hit.URL); err == nil {
						if real := u.Query().Get("uddg"); real != "" {
							hit.URL = real
						}
					}
				}
				return
			}
		}
		if node.Type == html.ElementNode && node.Data == "a" {
			class := getAttr(node, "class")
			if strings.Contains(class, "result__snippet") {
				hit.Snippet = textContent(node)
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
	}
	extract(n)
	return hit
}

func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

func strSet(list []string) map[string]bool {
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[strings.ToLower(s)] = true
	}
	return m
}

func domainMatches(rawURL string, allowed, blocked map[string]bool) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true // can't parse, let it through
	}
	host := strings.ToLower(u.Hostname())

	if len(allowed) > 0 {
		for domain := range allowed {
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return true
			}
		}
		return false
	}

	if len(blocked) > 0 {
		for domain := range blocked {
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return false
			}
		}
	}
	return true
}

func detectDDGCaptcha(body []byte) bool {
	s := strings.ToLower(string(body))
	for _, indicator := range []string{"captcha", "challenge", "blocked", "unusual traffic", "robot"} {
		if strings.Contains(s, indicator) {
			return true
		}
	}
	return false
}
