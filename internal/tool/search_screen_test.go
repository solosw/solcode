package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/systemone"
)

func TestJevSearchScreenerKeepsRelevantHits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{
				"candidate_0": map[string]any{"type": "noul", "noul": 0.9},
				"candidate_1": map[string]any{"type": "noul", "noul": 0.1},
			},
		})
	}))
	defer server.Close()

	decider := systemone.NewDecider(systemone.NewClient(systemone.Options{
		BaseURL:      server.URL,
		APIKey:       "k",
		HTTPClient:   server.Client(),
		DisableCache: true,
	}))
	screener := newJevSearchScreener(decider, 0.5)
	if screener == nil {
		t.Fatal("expected screener")
	}

	hits := []SearchHit{
		{Title: "Relevant", URL: "https://a.example", Snippet: "matches"},
		{Title: "Noise", URL: "https://b.example", Snippet: "unrelated"},
	}
	got := screener.Screen(context.Background(), "agent skills", hits)
	if len(got) != 1 || got[0].URL != "https://a.example" {
		t.Fatalf("got %#v", got)
	}
}

func TestJevSearchScreenerFailsOpenWhenEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{
				"candidate_0": map[string]any{"type": "noul", "noul": 0.1},
			},
		})
	}))
	defer server.Close()

	decider := systemone.NewDecider(systemone.NewClient(systemone.Options{
		BaseURL:      server.URL,
		APIKey:       "k",
		HTTPClient:   server.Client(),
		DisableCache: true,
	}))
	screener := newJevSearchScreener(decider, 0.5)
	hits := []SearchHit{{Title: "Only", URL: "https://only.example", Snippet: "x"}}
	got := screener.Screen(context.Background(), "query", hits)
	if len(got) != 1 || got[0].URL != "https://only.example" {
		t.Fatalf("fail-open got %#v", got)
	}
}

func TestNewJevSearchScreenerNilWhenDisabled(t *testing.T) {
	if got := newJevSearchScreener(nil, 0.5); got != nil {
		t.Fatalf("got %#v", got)
	}
	if got := newJevSearchScreener(systemone.NewDecider(nil), 0.5); got != nil {
		t.Fatalf("got %#v", got)
	}
}

func TestSearchHitDescription(t *testing.T) {
	desc := searchHitDescription(SearchHit{Title: "T", URL: "https://u", Snippet: "S"})
	if !strings.Contains(desc, "URL: https://u") || !strings.Contains(desc, "Title: T") {
		t.Fatalf("desc = %q", desc)
	}
}
