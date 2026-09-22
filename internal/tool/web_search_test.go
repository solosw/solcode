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
	wt := newWebSearchTool(fake, nil, nil, nil).(*webSearchTool)
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
	wt := newWebSearchTool(&fakeWebSearcher{err: context.DeadlineExceeded}, fallback, nil, nil).(*webSearchTool)
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

func TestWebSearchUsesBaiduFallbackAfterBackendPanic(t *testing.T) {
	fallback := &fakeBaiduSearcher{hits: []SearchHit{{Title: "Baidu panic fallback", URL: "https://example.cn/b", Snippet: "fallback"}}}
	wt := newWebSearchTool(&fakeWebSearcher{err: errors.New("websearch backend panicked: runtime error: invalid memory address or nil pointer dereference")}, fallback, nil, nil).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"text"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Text, "Baidu panic fallback") {
		t.Fatalf("result = %+v", result)
	}
	if fallback.query != "agent skills" {
		t.Fatalf("fallback query = %q", fallback.query)
	}
}

func TestWebSearchDoesNotUseBaiduFallbackForNonTimeoutOrNonText(t *testing.T) {
	fallback := &fakeBaiduSearcher{hits: []SearchHit{{Title: "should not appear"}}}
	wt := newWebSearchTool(&fakeWebSearcher{err: errors.New("backend unavailable")}, fallback, nil, nil).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"text"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || fallback.query != "" {
		t.Fatalf("non-timeout result/fallback = %+v / %+v", result, fallback)
	}

	wt = newWebSearchTool(&fakeWebSearcher{err: context.DeadlineExceeded}, fallback, nil, nil).(*webSearchTool)
	result, err = wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"news"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || fallback.query != "" {
		t.Fatalf("non-text result/fallback = %+v / %+v", result, fallback)
	}
}

func TestWebSearchRejectsUnknownCategory(t *testing.T) {
	wt := newWebSearchTool(&fakeWebSearcher{}, nil, nil, nil).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"invalid"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Text, "unsupported category") {
		t.Fatalf("result = %+v", result)
	}
}

func TestWebSearchReturnsToolErrorWhenBackendFails(t *testing.T) {
	wt := newWebSearchTool(&fakeWebSearcher{err: errors.New("backend unavailable")}, nil, nil, nil).(*webSearchTool)
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

type fakeParallelSearcher struct {
	hits  []SearchHit
	err   error
	query string
	limit int
}

func (f *fakeParallelSearcher) Search(_ context.Context, query string, maxResults int) ([]SearchHit, error) {
	f.query = query
	f.limit = maxResults
	return f.hits, f.err
}

type fakeSearchScreener struct {
	query string
	hits  []SearchHit
	out   []SearchHit
}

func (f *fakeSearchScreener) Screen(_ context.Context, query string, hits []SearchHit) []SearchHit {
	f.query = query
	f.hits = append([]SearchHit(nil), hits...)
	if f.out != nil {
		return f.out
	}
	return hits
}

func TestWebSearchMergesParallelAheadOfMetasearch(t *testing.T) {
	fake := &fakeWebSearcher{results: []websearch.SearchResult{
		{Category: websearch.CategoryText, Text: &websearch.TextResult{Title: "Meta", Href: "https://meta.example/a", Body: "from meta"}},
		{Category: websearch.CategoryText, Text: &websearch.TextResult{Title: "Dup", Href: "https://parallel.example/a", Body: "dup from meta"}},
	}}
	parallel := &fakeParallelSearcher{hits: []SearchHit{
		{Title: "Parallel", URL: "https://parallel.example/a", Snippet: "from parallel"},
		{Title: "OnlyParallel", URL: "https://parallel.example/b", Snippet: "only"},
	}}
	wt := newWebSearchTool(fake, nil, parallel, nil).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"text","max_results":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result = %+v", result)
	}
	if parallel.query != "agent skills" || parallel.limit != 3 {
		t.Fatalf("parallel query/limit = %q/%d", parallel.query, parallel.limit)
	}
	if !strings.Contains(result.Text, "1. Parallel") || !strings.Contains(result.Text, "2. OnlyParallel") || !strings.Contains(result.Text, "3. Meta") {
		t.Fatalf("merged output = %q", result.Text)
	}
	if strings.Contains(result.Text, "Dup") {
		t.Fatalf("duplicate parallel URL should be dropped: %q", result.Text)
	}
}

func TestWebSearchScreensResultsWithQuery(t *testing.T) {
	fake := &fakeWebSearcher{results: []websearch.SearchResult{
		{Category: websearch.CategoryText, Text: &websearch.TextResult{Title: "Keep", Href: "https://keep.example", Body: "yes"}},
		{Category: websearch.CategoryText, Text: &websearch.TextResult{Title: "Drop", Href: "https://drop.example", Body: "no"}},
	}}
	screener := &fakeSearchScreener{out: []SearchHit{{Title: "Keep", URL: "https://keep.example", Snippet: "yes"}}}
	wt := newWebSearchTool(fake, nil, nil, screener).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"text"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || screener.query != "agent skills" {
		t.Fatalf("result=%+v screener.query=%q", result, screener.query)
	}
	if !strings.Contains(result.Text, "Keep") || strings.Contains(result.Text, "Drop") {
		t.Fatalf("screened output = %q", result.Text)
	}
}

func TestMergeSearchHitsPrefersPrimary(t *testing.T) {
	got := mergeSearchHits(
		[]SearchHit{{Title: "P", URL: "https://p", Snippet: "p"}},
		[]SearchHit{{Title: "S", URL: "https://s", Snippet: "s"}, {Title: "Dup", URL: "https://p", Snippet: "dup"}},
		2,
	)
	if len(got) != 2 || got[0].URL != "https://p" || got[1].URL != "https://s" {
		t.Fatalf("got %#v", got)
	}
}
