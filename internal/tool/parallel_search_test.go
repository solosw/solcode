package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseParallelMCPToolResult(t *testing.T) {
	raw := []byte(`{
	  "jsonrpc":"2.0","id":2,
	  "result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"Hello\",\"url\":\"https://example.com/a\",\"excerpts\":[\"one\",\"two\"]},{\"title\":\"\",\"url\":\"https://example.com/b\"}]}"}]}
	}`)
	hits, err := parseParallelMCPToolResult(raw, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %#v", hits)
	}
	if hits[0].Title != "Hello" || hits[0].URL != "https://example.com/a" {
		t.Fatalf("hit = %#v", hits[0])
	}
	if !strings.Contains(hits[0].Snippet, "one") {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
}

func TestParallelMCPSearcherSearch(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("User-Agent") == "" {
			t.Fatalf("missing User-Agent")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		method, _ := body["method"].(string)
		switch method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"Parallel Web Search MCP Server","version":"1.0.0"}}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"A\",\"url\":\"https://a.example\",\"snippet\":\"alpha\"},{\"title\":\"B\",\"url\":\"https://b.example\",\"description\":\"beta\"}]}"}]}}`))
		default:
			http.Error(w, "unexpected method "+method, http.StatusBadRequest)
		}
	}))
	defer server.Close()

	searcher := &parallelMCPSearcher{client: server.Client(), url: server.URL}
	hits, err := searcher.Search(context.Background(), "hello world", 5)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want initialize + tools/call", calls)
	}
	if len(hits) != 2 || hits[0].Title != "A" || hits[1].Snippet != "beta" {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestStringSliceUnmarshal(t *testing.T) {
	var one stringSlice
	if err := json.Unmarshal([]byte(`"solo"`), &one); err != nil || len(one) != 1 || one[0] != "solo" {
		t.Fatalf("one = %#v err=%v", one, err)
	}
	var many stringSlice
	if err := json.Unmarshal([]byte(`["a","b"]`), &many); err != nil || len(many) != 2 {
		t.Fatalf("many = %#v err=%v", many, err)
	}
}
