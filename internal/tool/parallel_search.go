package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/httpproxy"
)

const (
	parallelMCPURL      = "https://search.parallel.ai/mcp"
	parallelMCPToolName = "web_search"
	parallelUserAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
)

// parallelSearcher is the keyless Parallel Search MCP backend.
type parallelSearcher interface {
	Search(ctx context.Context, query string, maxResults int) ([]SearchHit, error)
}

type parallelMCPSearcher struct {
	client *http.Client
	url    string
}

func newParallelMCPSearcher(client *http.Client) *parallelMCPSearcher {
	if client == nil {
		client = httpproxy.NewClient(30 * time.Second)
	}
	return &parallelMCPSearcher{client: client, url: parallelMCPURL}
}

func (p *parallelMCPSearcher) Search(ctx context.Context, query string, maxResults int) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}

	// Cloudflare on search.parallel.ai bans non-browser signatures (Error 1010).
	// Initialize is cheap and keeps the session shape MCP servers expect.
	if err := p.initialize(ctx); err != nil {
		return nil, err
	}

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": parallelMCPToolName,
			"arguments": map[string]any{
				"objective":      query,
				"search_queries": []string{query},
			},
		},
	}
	raw, err := p.post(ctx, payload)
	if err != nil {
		return nil, err
	}

	hits, err := parseParallelMCPToolResult(raw, maxResults)
	if err != nil {
		return nil, err
	}
	return hits, nil
}

func (p *parallelMCPSearcher) initialize(ctx context.Context) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "solcode",
				"version": "0.1.0",
			},
		},
	}
	_, err := p.post(ctx, payload)
	return err
}

func (p *parallelMCPSearcher) post(ctx context.Context, payload map[string]any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", parallelUserAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Parallel MCP: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("Parallel MCP: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Parallel MCP: HTTP %d: %s", resp.StatusCode, truncateRunes(string(raw), 240))
	}
	return raw, nil
}

type parallelMCPResponse struct {
	Error  *parallelMCPError `json:"error"`
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
}

type parallelMCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type parallelSearchPayload struct {
	Results []parallelSearchResult `json:"results"`
}

type parallelSearchResult struct {
	Title       string      `json:"title"`
	URL         string      `json:"url"`
	Snippet     string      `json:"snippet"`
	Description string      `json:"description"`
	Excerpts    stringSlice `json:"excerpts"`
}

func parseParallelMCPToolResult(raw []byte, limit int) ([]SearchHit, error) {
	var resp parallelMCPResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("Parallel MCP: decode response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("Parallel MCP: %s", strings.TrimSpace(resp.Error.Message))
	}
	if resp.Result.IsError {
		return nil, fmt.Errorf("Parallel MCP: tool returned an error")
	}

	out := make([]SearchHit, 0, limit)
	seen := make(map[string]bool)
	for _, block := range resp.Result.Content {
		if block.Type != "" && block.Type != "text" {
			continue
		}
		text := strings.TrimSpace(block.Text)
		if text == "" {
			continue
		}
		var payload parallelSearchPayload
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			return nil, fmt.Errorf("Parallel MCP: decode results: %w", err)
		}
		for _, item := range payload.Results {
			hit, ok := parallelResultToHit(item)
			if !ok || seen[hit.URL] {
				continue
			}
			seen[hit.URL] = true
			out = append(out, hit)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func parallelResultToHit(r parallelSearchResult) (SearchHit, bool) {
	title := collapseWhitespace(strings.TrimSpace(r.Title))
	rawURL := strings.TrimSpace(r.URL)
	if title == "" || rawURL == "" {
		return SearchHit{}, false
	}
	snippet := strings.TrimSpace(r.Snippet)
	if snippet == "" {
		snippet = strings.TrimSpace(strings.Join(r.Excerpts, "\n"))
	}
	if snippet == "" {
		snippet = strings.TrimSpace(r.Description)
	}
	return SearchHit{
		Title:   title,
		URL:     rawURL,
		Snippet: collapseWhitespace(excerpt(snippet, 400)),
	}, true
}

// stringSlice unmarshals from either a JSON array of strings or a single string.
type stringSlice []string

func (s *stringSlice) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*s = nil
		return nil
	}
	if b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func excerpt(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func truncateRunes(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
