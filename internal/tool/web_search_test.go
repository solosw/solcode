package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	websearch "github.com/proiceremo/websearch"
)

type fakeWebSearcher struct {
	results []websearch.SearchResult
	err     error
	query   string
	opts    websearch.SearchOptions
}

func (f *fakeWebSearcher) Search(_ context.Context, query string, opts websearch.SearchOptions) ([]websearch.SearchResult, error) {
	f.query = query
	f.opts = opts
	return f.results, f.err
}

type fakeBaiduSearcher struct {
	hits  []SearchHit
	err   error
	query string
	limit int
}

func (f *fakeBaiduSearcher) Search(_ context.Context, query string, maxResults int) ([]SearchHit, error) {
	f.query = query
	f.limit = maxResults
	return f.hits, f.err
}

func TestWebSearchUsesMetasearchCategoryAndDeduplicates(t *testing.T) {
	fake := &fakeWebSearcher{results: []websearch.SearchResult{
		{Category: websearch.CategoryNews, News: &websearch.NewsResult{Title: "News", URL: "https://news.example.com/a", Body: "News result"}},
		{Category: websearch.CategoryNews, News: &websearch.NewsResult{Title: "Duplicate", URL: "https://news.example.com/a", Body: "Duplicate result"}},
	}}
	wt := newWebSearchTool(fake, nil).(*webSearchTool)
	wt.timeout = time.Second

	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"news","max_results":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", result.Text)
	}
	if fake.query != "agent skills" {
		t.Fatalf("query = %q", fake.query)
	}
	if fake.opts.Category != websearch.CategoryNews || fake.opts.Backend != "auto" || fake.opts.MaxResults != 50 {
		t.Fatalf("options = %+v", fake.opts)
	}
	if !strings.Contains(result.Text, "News") || strings.Contains(result.Text, "Duplicate") {
		t.Fatalf("deduplicated output = %q", result.Text)
	}
	if !strings.Contains(result.Text, "Sources:") {
		t.Fatalf("missing source reminder: %q", result.Text)
	}
}

func TestWebSearchUsesBaiduFallbackOnlyAfterTextTimeout(t *testing.T) {
	fallback := &fakeBaiduSearcher{hits: []SearchHit{{Title: "Baidu result", URL: "https://example.cn/a", Snippet: "fallback"}}}
	wt := newWebSearchTool(&fakeWebSearcher{err: context.DeadlineExceeded}, fallback).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"text","max_results":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Text, "Baidu result") {
		t.Fatalf("result = %+v", result)
	}
	if fallback.query != "agent skills" || fallback.limit != 7 {
		t.Fatalf("fallback query/limit = %q/%d", fallback.query, fallback.limit)
	}
}

func TestWebSearchDoesNotUseBaiduFallbackForNonTimeoutOrNonText(t *testing.T) {
	fallback := &fakeBaiduSearcher{hits: []SearchHit{{Title: "should not appear"}}}
	wt := newWebSearchTool(&fakeWebSearcher{err: errors.New("backend unavailable")}, fallback).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"text"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || fallback.query != "" {
		t.Fatalf("non-timeout result/fallback = %+v / %+v", result, fallback)
	}

	wt = newWebSearchTool(&fakeWebSearcher{err: context.DeadlineExceeded}, fallback).(*webSearchTool)
	result, err = wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"news"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || fallback.query != "" {
		t.Fatalf("non-text result/fallback = %+v / %+v", result, fallback)
	}
}

func TestWebSearchRejectsUnknownCategory(t *testing.T) {
	wt := newWebSearchTool(&fakeWebSearcher{}, nil).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"invalid"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Text, "unsupported category") {
		t.Fatalf("result = %+v", result)
	}
}

func TestWebSearchReturnsToolErrorWhenBackendFails(t *testing.T) {
	wt := newWebSearchTool(&fakeWebSearcher{err: errors.New("backend unavailable")}, nil).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Text, "backend unavailable") {
		t.Fatalf("result = %+v", result)
	}
}

func TestWebSearchCategory(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want websearch.Category
	}{
		{"", websearch.CategoryText},
		{"text", websearch.CategoryText},
		{"images", websearch.CategoryImages},
		{"news", websearch.CategoryNews},
		{"videos", websearch.CategoryVideos},
		{"books", websearch.CategoryBooks},
		{"research", websearch.CategoryResearch},
	} {
		got, err := webSearchCategory(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("webSearchCategory(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
		}
	}
}
