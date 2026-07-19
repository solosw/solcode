package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const baiduJSONSearchURL = "https://www.baidu.com/s"

// Polite pacing for Baidu JSON fallback. Jitter spreads load; it is not a
// rate-limit bypass (no proxy rotation, no multi-account, no burst retry).
const (
	baiduJitterMin = 150 * time.Millisecond
	baiduJitterMax = 700 * time.Millisecond
	// Single respectful backoff when Baidu returns 429 / Retry-After.
	baiduRateLimitMaxWait = 3 * time.Second
)

// Common desktop browser UAs. Rotating among real browser strings improves
// compatibility with sites that gate on UA; this is not captcha solving.
var baiduUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Edg/125.0.0.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14.5; rv:127.0) Gecko/20100101 Firefox/127.0",
}

type baiduJSONSearcher struct {
	client *http.Client
	base   string
	// Optional hooks for tests.
	sleep   func(context.Context, time.Duration) error
	pickUA  func() string
	jitter  func() time.Duration
	now     func() time.Time
	maxWait time.Duration
}

type baiduFeedEntry struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Abs   string `json:"abs"`
}

type baiduJSONResponse struct {
	Feed struct {
		Entry []baiduFeedEntry `json:"entry"`
	} `json:"feed"`
}

func newBaiduJSONSearcher(client *http.Client) *baiduJSONSearcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &baiduJSONSearcher{
		client:  client,
		base:    baiduJSONSearchURL,
		sleep:   sleepWithContext,
		pickUA:  randomBaiduUserAgent,
		jitter:  randomBaiduJitter,
		now:     time.Now,
		maxWait: baiduRateLimitMaxWait,
	}
}

// Search requests Baidu's documented JSON search response format. It uses a
// browser-like User-Agent pool and polite request jitter. Captcha / rate-limit
// pages are detected and returned as errors — this is not an anti-bot bypass.
func (b *baiduJSONSearcher) Search(ctx context.Context, query string, maxResults int) ([]SearchHit, error) {
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}

	if err := b.sleep(ctx, b.jitter()); err != nil {
		return nil, err
	}

	body, err := b.doSearch(ctx, query, maxResults, false)
	if err != nil {
		return nil, err
	}

	var decoded baiduJSONResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("parse Baidu JSON response: %w", err)
	}

	out := make([]SearchHit, 0, min(maxResults, len(decoded.Feed.Entry)))
	seen := make(map[string]bool, len(decoded.Feed.Entry))
	for _, entry := range decoded.Feed.Entry {
		resultURL := strings.TrimSpace(entry.URL)
		if resultURL == "" || seen[resultURL] {
			continue
		}
		seen[resultURL] = true
		title := normalizeBaiduText(entry.Title)
		if title == "" {
			title = resultURL
		}
		out = append(out, SearchHit{
			Title:   title,
			URL:     resultURL,
			Snippet: normalizeBaiduText(entry.Abs),
		})
		if len(out) == maxResults {
			break
		}
	}
	return out, nil
}

func (b *baiduJSONSearcher) doSearch(ctx context.Context, query string, maxResults int, retried bool) ([]byte, error) {
	endpoint, err := url.Parse(b.base)
	if err != nil {
		return nil, fmt.Errorf("build Baidu request URL: %w", err)
	}
	values := endpoint.Query()
	values.Set("wd", query)
	values.Set("rn", strconv.Itoa(maxResults))
	values.Set("pn", "0")
	values.Set("tn", "json")
	endpoint.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Baidu request: %w", err)
	}
	setBaiduRequestHeaders(req, b.pickUA())

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read Baidu response: %w", err)
	}

	// Prefer rate-limit handling over captcha when both could match (e.g. 429).
	if isBaiduRateLimited(resp.StatusCode, body) {
		if !retried {
			wait := baiduRetryAfter(resp.Header.Get("Retry-After"), b.now, b.maxWait)
			if wait > 0 {
				if err := b.sleep(ctx, wait); err != nil {
					return nil, err
				}
				return b.doSearch(ctx, query, maxResults, true)
			}
		}
		return nil, fmt.Errorf("Baidu rate limited (HTTP %d); not bypassing", resp.StatusCode)
	}

	if looksLikeBaiduChallengeHTML(string(body)) {
		return nil, fmt.Errorf("Baidu returned a captcha/challenge page (HTTP %d); not bypassing", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Baidu returned HTTP %d", resp.StatusCode)
	}

	// JSON endpoint can still return HTML under soft blocks.
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, fmt.Errorf("Baidu returned empty body")
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		if looksLikeBaiduChallengeHTML(trimmed) {
			return nil, fmt.Errorf("Baidu returned a captcha/challenge page instead of JSON; not bypassing")
		}
		return nil, fmt.Errorf("Baidu returned non-JSON body")
	}
	return body, nil
}

func setBaiduRequestHeaders(req *http.Request, ua string) {
	if ua == "" {
		ua = baiduUserAgents[0]
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", "https://www.baidu.com/")
	req.Header.Set("User-Agent", ua)
}

func randomBaiduUserAgent() string {
	return baiduUserAgents[rand.Intn(len(baiduUserAgents))]
}

func randomBaiduJitter() time.Duration {
	span := baiduJitterMax - baiduJitterMin
	if span <= 0 {
		return baiduJitterMin
	}
	return baiduJitterMin + time.Duration(rand.Int63n(int64(span)+1))
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isBaiduRateLimited(status int, body []byte) bool {
	if status == http.StatusTooManyRequests || status == 509 {
		return true
	}
	// Soft rate-limit signals — detect and fail/backoff, never bypass.
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "访问频率") ||
		strings.Contains(lower, "请求过于频繁") ||
		strings.Contains(lower, "too many requests")
}

func looksLikeBaiduChallengeHTML(body string) bool {
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "<html") && !strings.Contains(lower, "<!doctype") {
		// Non-HTML payloads that still point at Baidu challenge endpoints.
		return strings.Contains(lower, "wappass.baidu.com") ||
			(strings.Contains(lower, "captcha") && strings.Contains(lower, "baidu"))
	}
	markers := []string{
		"wappass.baidu.com",
		"passport.baidu.com",
		"验证码",
		"安全验证",
		"网络不给力",
		"请输入验证码",
		"captcha",
		"seccode",
		"verifycode",
	}
	for _, m := range markers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// baiduRetryAfter parses Retry-After and clamps to maxWait. Returns 0 when
// the header is missing or exceeds maxWait (caller should fail closed).
func baiduRetryAfter(header string, now func() time.Time, maxWait time.Duration) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" || maxWait <= 0 {
		// Small polite pause before one retry when header is absent.
		if maxWait > 0 {
			return min(maxWait, 500*time.Millisecond)
		}
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs <= 0 {
			return 0
		}
		d := time.Duration(secs) * time.Second
		if d > maxWait {
			return 0
		}
		return d
	}
	if t, err := http.ParseTime(header); err == nil {
		d := t.Sub(now())
		if d <= 0 {
			return 0
		}
		if d > maxWait {
			return 0
		}
		return d
	}
	return 0
}

func normalizeBaiduText(value string) string {
	value = html.UnescapeString(value)
	value = strings.ReplaceAll(value, "<em>", "")
	value = strings.ReplaceAll(value, "</em>", "")
	return strings.TrimSpace(value)
}
