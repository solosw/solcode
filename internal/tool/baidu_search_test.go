package tool

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestBaiduJSONSearcherBuildsJSONRequestAndParsesResults(t *testing.T) {
	var gotUA string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "www.baidu.com" || req.URL.Path != "/s" {
			t.Fatalf("URL = %s", req.URL)
		}
		if req.URL.Query().Get("wd") != "agent skills" || req.URL.Query().Get("tn") != "json" || req.URL.Query().Get("rn") != "2" {
			t.Fatalf("query = %s", req.URL.RawQuery)
		}
		gotUA = req.Header.Get("User-Agent")
		if gotUA == "" || gotUA == "solcode/1.0" {
			t.Fatalf("User-Agent = %q", gotUA)
		}
		if req.Header.Get("Referer") != "https://www.baidu.com/" {
			t.Fatalf("Referer = %q", req.Header.Get("Referer"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"feed":{"entry":[
					{"title":"<em>Agent</em> &amp; Skills","url":"https://example.cn/a","abs":"A &amp; B"},
					{"title":"Duplicate","url":"https://example.cn/a","abs":"ignored"}
				]}
			}`)),
			Request: req,
		}, nil
	})}
	searcher := newBaiduJSONSearcher(client)
	searcher.sleep = func(context.Context, time.Duration) error { return nil }
	searcher.jitter = func() time.Duration { return 0 }
	searcher.pickUA = func() string {
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	}

	hits, err := searcher.Search(context.Background(), "agent skills", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "Agent & Skills" || hits[0].Snippet != "A & B" {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(gotUA, "Chrome/126") {
		t.Fatalf("User-Agent = %q", gotUA)
	}
}

func TestBaiduJSONSearcherAppliesJitter(t *testing.T) {
	var slept time.Duration
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"feed":{"entry":[]}}`)),
			Request:    req,
		}, nil
	})}
	searcher := newBaiduJSONSearcher(client)
	searcher.jitter = func() time.Duration { return 321 * time.Millisecond }
	searcher.sleep = func(_ context.Context, d time.Duration) error {
		slept = d
		return nil
	}
	if _, err := searcher.Search(context.Background(), "q", 1); err != nil {
		t.Fatal(err)
	}
	if slept != 321*time.Millisecond {
		t.Fatalf("slept = %v", slept)
	}
}

func TestBaiduJSONSearcherDetectsCaptcha(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`<!DOCTYPE html><html><body>
				<script src="https://wappass.baidu.com/static/captcha/t.js"></script>
				请输入验证码
			</body></html>`)),
			Request: req,
		}, nil
	})}
	searcher := newBaiduJSONSearcher(client)
	searcher.sleep = func(context.Context, time.Duration) error { return nil }
	searcher.jitter = func() time.Duration { return 0 }

	_, err := searcher.Search(context.Background(), "q", 1)
	if err == nil || !strings.Contains(err.Error(), "captcha") {
		t.Fatalf("err = %v", err)
	}
}

func TestBaiduJSONSearcherRateLimitSingleBackoff(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n == 1 {
			h := make(http.Header)
			h.Set("Retry-After", "1")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     h,
				Body:       io.NopCloser(strings.NewReader("too many requests")),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"feed":{"entry":[
				{"title":"OK","url":"https://example.cn/ok","abs":"done"}
			]}}`)),
			Request: req,
		}, nil
	})}
	searcher := newBaiduJSONSearcher(client)
	var sleeps []time.Duration
	searcher.sleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	searcher.jitter = func() time.Duration { return 10 * time.Millisecond }
	searcher.maxWait = 3 * time.Second

	hits, err := searcher.Search(context.Background(), "q", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].URL != "https://example.cn/ok" {
		t.Fatalf("hits = %+v", hits)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
	// jitter + Retry-After(1s)
	if len(sleeps) < 2 || sleeps[1] != time.Second {
		t.Fatalf("sleeps = %v", sleeps)
	}
}

func TestBaiduJSONSearcherRateLimitExceedsMaxWaitFailsClosed(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "30")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader("too many requests")),
			Request:    req,
		}, nil
	})}
	searcher := newBaiduJSONSearcher(client)
	searcher.sleep = func(context.Context, time.Duration) error { return nil }
	searcher.jitter = func() time.Duration { return 0 }
	searcher.maxWait = 3 * time.Second

	_, err := searcher.Search(context.Background(), "q", 1)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
}

func TestRandomBaiduUserAgentFromPool(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		ua := randomBaiduUserAgent()
		found := false
		for _, known := range baiduUserAgents {
			if ua == known {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("unexpected UA: %q", ua)
		}
		seen[ua] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected variety, saw %d", len(seen))
	}
}

func TestBaiduRetryAfterClamp(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	if d := baiduRetryAfter("2", now, 3*time.Second); d != 2*time.Second {
		t.Fatalf("seconds = %v", d)
	}
	if d := baiduRetryAfter("10", now, 3*time.Second); d != 0 {
		t.Fatalf("over max = %v", d)
	}
	if d := baiduRetryAfter("", now, 3*time.Second); d != 500*time.Millisecond {
		t.Fatalf("empty = %v", d)
	}
}
