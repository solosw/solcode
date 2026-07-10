package unit_tests

import (
	"io"
	"net/http"
	"testing"

	internalmcp "github.com/solosw/solcode/internal/mcp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHeaderRoundTripperInjectsHeaders(t *testing.T) {
	hit := false
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hit = true
		if req.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("Authorization header = %q", req.Header.Get("Authorization"))
		}
		if req.Header.Get("X-Test") != "1" {
			t.Fatalf("X-Test header = %q", req.Header.Get("X-Test"))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(nil), Header: make(http.Header)}, nil
	})

	rt := internalmcp.NewTestHeaderRoundTripper(base, map[string]string{"Authorization": "Bearer token", "X-Test": "1"})
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}
	_, err = rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() = %v", err)
	}
	if !hit {
		t.Fatal("expected base round tripper to be called")
	}
}
