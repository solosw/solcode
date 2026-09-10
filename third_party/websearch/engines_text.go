package websearch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// NewBingText returns the Bing HTML text search engine.
func NewBingText() SearchEngine {
	return &XPathEngine{
		name:         "bing",
		category:     CategoryText,
		provider:     "bing",
		priority:     1,
		SearchURL:    "https://www.bing.com/search",
		SearchMethod: http.MethodGet,
		ItemsXPath:   "//li[contains(@class,'b_algo')]",
		ElementsMap: map[string]string{
			"title": ".//h2//a//text()",
			"href":  ".//h2//a/@href",
			"body":  ".//div[contains(@class,'b_caption')]//p//text()",
		},
		ExtraHeaders: map[string]string{
			"Accept-Language": "en-US,en;q=0.9",
		},
		BuildPayload: func(query string, opts SearchOptions) (url.Values, url.Values) {
			params := url.Values{
				"q": {query},
			}
			if opts.Page > 1 {
				params.Set("first", itoa((opts.Page-1)*10+1))
			}
			switch opts.SafeSearch {
			case "on", "strict":
				params.Set("adlt", "strict")
			case "off":
				params.Set("adlt", "off")
			}
			if opts.TimeLimit != "" {
				// ez1=day, ez2=week, ez3=month; year is approximate via ez5.
				tlMap := map[string]string{"d": "ez1", "w": "ez2", "m": "ez3", "y": "ez5"}
				if v, ok := tlMap[opts.TimeLimit]; ok {
					params.Set("filters", `ex1:"`+v+`"`)
				}
			}
			return params, nil
		},
		PostProcess: func(results []SearchResult) []SearchResult {
			var filtered []SearchResult
			for _, r := range results {
				if r.Text == nil {
					continue
				}
				href := strings.TrimSpace(r.Text.Href)
				if href == "" || strings.HasPrefix(href, "https://www.bing.com/aclick?") {
					continue
				}
				if strings.Contains(href, "bing.com/ck/") || strings.Contains(href, "bing.com/ck/a?") {
					href = unwrapBingURL(href)
				}
				r.Text.Href = href
				if r.Text.Title == "" && r.Text.Href == "" {
					continue
				}
				filtered = append(filtered, r)
			}
			return filtered
		},
	}
}

// NewDuckDuckGoText returns the DuckDuckGo HTML text search engine.
func NewDuckDuckGoText() SearchEngine {
	return &XPathEngine{
		name:         "duckduckgo",
		category:     CategoryText,
		provider:     "bing",
		priority:     1,
		SearchURL:    "https://html.duckduckgo.com/html/",
		SearchMethod: http.MethodPost,
		ItemsXPath:   "//div[contains(@class, 'body')]",
		ElementsMap: map[string]string{
			"title": ".//h2//text()",
			"href":  "./a/@href",
			"body":  "./a//text()",
		},
		BuildPayload: func(query string, opts SearchOptions) (url.Values, url.Values) {
			data := url.Values{
				"q": {query},
				"b": {""},
				"l": {opts.Region},
			}
			if opts.Page > 1 {
				data.Set("s", itoa(10+(opts.Page-2)*15))
			}
			if opts.TimeLimit != "" {
				data.Set("df", opts.TimeLimit)
			}
			return nil, data
		},
		PostProcess: func(results []SearchResult) []SearchResult {
			var filtered []SearchResult
			for _, r := range results {
				if r.Text != nil && !strings.HasPrefix(r.Text.Href, "https://duckduckgo.com/y.js?") {
					filtered = append(filtered, r)
				}
			}
			return filtered
		},
	}
}

// NewBraveText returns the Brave text search engine.
func NewBraveText() SearchEngine {
	return &XPathEngine{
		name:         "brave",
		category:     CategoryText,
		provider:     "brave",
		priority:     1,
		SearchURL:    "https://search.brave.com/search",
		SearchMethod: http.MethodGet,
		ItemsXPath:   "//div[@data-type='web']",
		ElementsMap: map[string]string{
			"title": ".//div[(contains(@class,'title') or contains(@class,'sitename-container')) and position()=last()]//text()",
			"href":  ".//a[div[contains(@class, 'title')]]/@href",
			"body":  ".//div[contains(@class, 'snippet')]//div[contains(@class, 'content')]//text()",
		},
		BuildPayload: func(query string, opts SearchOptions) (url.Values, url.Values) {
			params := url.Values{
				"q":      {query},
				"source": {"web"},
			}
			if opts.TimeLimit != "" {
				tlMap := map[string]string{"d": "pd", "w": "pw", "m": "pm", "y": "py"}
				if v, ok := tlMap[opts.TimeLimit]; ok {
					params.Set("tf", v)
				}
			}
			if opts.Page > 1 {
				params.Set("offset", itoa(opts.Page-1))
			}
			return params, nil
		},
	}
}

// NewYahooText returns the Yahoo text search engine.
func NewYahooText() SearchEngine {
	return &XPathEngine{
		name:         "yahoo",
		category:     CategoryText,
		provider:     "bing",
		priority:     1,
		SearchURL:    "https://search.yahoo.com/search",
		SearchMethod: http.MethodGet,
		ItemsXPath:   "//div[contains(@class, 'relsrch')]",
		ElementsMap: map[string]string{
			"title": ".//div[contains(@class, 'Title')]//h3//text()",
			"href":  ".//div[contains(@class, 'Title')]//a/@href",
			"body":  ".//div[contains(@class, 'Text')]//text()",
		},
		BuildPayload: func(query string, opts SearchOptions) (url.Values, url.Values) {
			params := url.Values{"p": {query}}
			if opts.Page > 1 {
				params.Set("b", itoa((opts.Page-1)*7+1))
			}
			if opts.TimeLimit != "" {
				params.Set("btf", opts.TimeLimit)
			}
			return params, nil
		},
		PostProcess: func(results []SearchResult) []SearchResult {
			var filtered []SearchResult
			for _, r := range results {
				if r.Text == nil {
					continue
				}
				if strings.HasPrefix(r.Text.Href, "https://www.bing.com/aclick?") {
					continue
				}
				if strings.Contains(r.Text.Href, "/RU=") {
					r.Text.Href = extractYahooURL(r.Text.Href)
				}
				filtered = append(filtered, r)
			}
			return filtered
		},
	}
}

// NewMojeekText returns the Mojeek text search engine.
func NewMojeekText() SearchEngine {
	return &XPathEngine{
		name:         "mojeek",
		category:     CategoryText,
		provider:     "mojeek",
		priority:     1,
		SearchURL:    "https://www.mojeek.com/search",
		SearchMethod: http.MethodGet,
		ItemsXPath:   "//ul[contains(@class, 'results')]/li",
		ElementsMap: map[string]string{
			"title": ".//h2//text()",
			"href":  ".//h2/a/@href",
			"body":  ".//p[@class='s']//text()",
		},
		BuildPayload: func(query string, opts SearchOptions) (url.Values, url.Values) {
			params := url.Values{"q": {query}}
			if opts.SafeSearch == "on" {
				params.Set("safe", "1")
			}
			if opts.Page > 1 {
				params.Set("s", itoa((opts.Page-1)*10+1))
			}
			return params, nil
		},
	}
}

// extractYahooURL extracts the real URL from a Yahoo redirect URL.
func extractYahooURL(u string) string {
	parts := strings.SplitN(u, "/RU=", 2)
	if len(parts) < 2 {
		return u
	}
	t := parts[1]
	for _, sep := range []string{"/RK=", "/RS="} {
		if idx := strings.Index(t, sep); idx >= 0 {
			t = t[:idx]
		}
	}
	decoded, err := url.QueryUnescape(t)
	if err != nil {
		return t
	}
	return decoded
}

// unwrapBingURL decodes the Bing-wrapped redirect URL.
func unwrapBingURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	uVals := parsed.Query()["u"]
	if len(uVals) == 0 || len(uVals[0]) <= 2 {
		return rawURL
	}
	b64Part := uVals[0][2:]
	// Pad to multiple of 4
	if mod := len(b64Part) % 4; mod != 0 {
		b64Part += strings.Repeat("=", 4-mod)
	}
	decoded, err := base64.URLEncoding.DecodeString(b64Part)
	if err != nil {
		return rawURL
	}
	return string(decoded)
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

// WikipediaEngine implements text search via the Wikipedia opensearch API.
type WikipediaEngine struct {
	priority float64
}

func NewWikipediaText() SearchEngine {
	return &WikipediaEngine{priority: 2}
}

func (w *WikipediaEngine) Name() string       { return "wikipedia" }
func (w *WikipediaEngine) Category() Category  { return CategoryText }
func (w *WikipediaEngine) Provider() string    { return "wikipedia" }
func (w *WikipediaEngine) Priority() float64   { return w.priority }

func (w *WikipediaEngine) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	client := httpClient(opts.Proxy, opts.Timeout)

	_, lang := splitRegion(opts.Region)

	// Step 1: opensearch to get article title + URL
	searchURL := fmt.Sprintf(
		"https://%s.wikipedia.org/w/api.php?action=opensearch&profile=fuzzy&limit=1&search=%s",
		lang, url.QueryEscape(query),
	)
	body, err := doRequest(ctx, client, http.MethodGet, searchURL, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("wikipedia opensearch: %w", err)
	}

	var openSearch []json.RawMessage
	if err := json.Unmarshal(body, &openSearch); err != nil || len(openSearch) < 4 {
		return nil, nil // No results
	}

	var titles []string
	if err := json.Unmarshal(openSearch[1], &titles); err != nil || len(titles) == 0 {
		return nil, nil
	}
	var urls []string
	if err := json.Unmarshal(openSearch[3], &urls); err != nil || len(urls) == 0 {
		return nil, nil
	}

	title := titles[0]
	href := urls[0]

	// Step 2: get extract for the article
	extractURL := fmt.Sprintf(
		"https://%s.wikipedia.org/w/api.php?action=query&format=json&prop=extracts&titles=%s&explaintext=0&exintro=0&redirects=1",
		lang, url.QueryEscape(title),
	)
	extractBody, err := doRequest(ctx, client, http.MethodGet, extractURL, nil, nil, nil)
	if err != nil {
		// Return result without body on extract failure
		return []SearchResult{{
			Category: CategoryText,
			Text:     &TextResult{Title: title, Href: href},
		}}, nil
	}

	var extractResp struct {
		Query struct {
			Pages map[string]struct {
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(extractBody, &extractResp); err != nil {
		return []SearchResult{{
			Category: CategoryText,
			Text:     &TextResult{Title: title, Href: href},
		}}, nil
	}

	var bodyText string
	for _, page := range extractResp.Query.Pages {
		bodyText = page.Extract
		break
	}

	// Skip disambiguation pages
	if strings.Contains(bodyText, "may refer to:") {
		return nil, nil
	}

	return []SearchResult{{
		Category: CategoryText,
		Text:     &TextResult{Title: title, Href: href, Body: NormalizeText(bodyText)},
	}}, nil
}

// splitRegion splits "us-en" into ("us", "en").
func splitRegion(region string) (string, string) {
	parts := strings.SplitN(strings.ToLower(region), "-", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return region, "en"
}

// GrokipediaEngine implements text search via the Grokipedia API.
type GrokipediaEngine struct {
	priority float64
}

// NewGrokipedia returns the Grokipedia text search engine.
func NewGrokipedia() SearchEngine {
	return &GrokipediaEngine{priority: 1.9}
}

func (e *GrokipediaEngine) Name() string       { return "grokipedia" }
func (e *GrokipediaEngine) Category() Category  { return CategoryText }
func (e *GrokipediaEngine) Provider() string    { return "grokipedia" }
func (e *GrokipediaEngine) Priority() float64   { return e.priority }

func (e *GrokipediaEngine) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	client := httpClient(opts.Proxy, opts.Timeout)

	searchURL := fmt.Sprintf("https://grokipedia.com/api/typeahead?query=%s&limit=1", query)
	body, err := doRequest(ctx, client, http.MethodGet, searchURL, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("grokipedia API: %w", err)
	}

	var resp struct {
		Results []struct {
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			Slug    string `json:"slug"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("grokipedia unmarshal: %w", err)
	}

	if len(resp.Results) == 0 {
		return nil, nil
	}

	item := resp.Results[0]
	title := strings.Trim(item.Title, "_")
	snippet := item.Snippet
	if idx := strings.Index(snippet, "\n\n"); idx != -1 {
		snippet = snippet[idx+2:]
	}

	return []SearchResult{{
		Category: CategoryText,
		Text: &TextResult{
			Title: NormalizeText(title),
			Body:  NormalizeText(snippet),
			Href:  fmt.Sprintf("https://grokipedia.com/page/%s", item.Slug),
		},
	}}, nil
}
