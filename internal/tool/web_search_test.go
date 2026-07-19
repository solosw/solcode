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

func TestWebSearchUsesMetasearchCategoryAndDeduplicates(t *testing.T) {
	fake := &fakeWebSearcher{results: []websearch.SearchResult{
		{Category: websearch.CategoryNews, News: &websearch.NewsResult{Title: "News", URL: "https://news.example.com/a", Body: "News result"}},
		{Category: websearch.CategoryNews, News: &websearch.NewsResult{Title: "Duplicate", URL: "https://news.example.com/a", Body: "Duplicate result"}},
	}}
	wt := newWebSearchTool(fake).(*webSearchTool)
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

func TestWebSearchRejectsUnknownCategory(t *testing.T) {
	wt := newWebSearchTool(&fakeWebSearcher{}).(*webSearchTool)
	result, err := wt.Invoke(context.Background(), nil, json.RawMessage(`{"query":"agent skills","category":"invalid"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Text, "unsupported category") {
		t.Fatalf("result = %+v", result)
	}
}

func TestWebSearchReturnsToolErrorWhenBackendFails(t *testing.T) {
	wt := newWebSearchTool(&fakeWebSearcher{err: errors.New("backend unavailable")}).(*webSearchTool)
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
