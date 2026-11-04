package organizer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
)

func TestAPIGeneratorProtocols(t *testing.T) {
	for _, tc := range []struct{ format, path, response string }{
		{"openai", "/v1/chat/completions", `{"choices":[{"message":{"role":"assistant","content":"organized"},"finish_reason":"stop"}]}`},
		{"anthropic", "/v1/messages", `{"id":"msg_test","type":"message","role":"assistant","model":"org-model","content":[{"type":"text","text":"organized"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`},
		{"responses", "/v1/responses", `{"id":"resp_test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"organized"}]}]}`},
	} {
		t.Run(tc.format, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %s, want %s", r.URL.Path, tc.path)
				}
				if r.Header.Get("Authorization") != "Bearer organizer-key" && r.Header.Get("X-Api-Key") != "organizer-key" {
					t.Error("missing organizer credentials")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "org-model" {
					t.Errorf("model = %v", body["model"])
				}
				raw, _ := json.Marshal(body)
				if !strings.Contains(string(raw), "session transcript") || !strings.Contains(string(raw), "organizer system") {
					t.Errorf("missing prompt: %s", raw)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()
			gen := NewAPIGenerator(config.OrganizerConfig{Model: "org-model", APIKey: "organizer-key", BaseURL: srv.URL + "/v1", APIFormat: tc.format})
			if !gen.Ready() {
				t.Fatal("configured API not ready")
			}
			text, err := gen.Generate(context.Background(), GenerateRequest{System: "organizer system", User: "session transcript", MaxTokens: 123})
			if err != nil || text != "organized" {
				t.Fatalf("Generate = %q, %v", text, err)
			}
		})
	}
}

func TestAPIGeneratorUnavailableAndErrors(t *testing.T) {
	gen := NewAPIGenerator(config.OrganizerConfig{Model: "model"})
	if gen.Ready() {
		t.Fatal("API must not inherit chat defaults")
	}
	if _, err := gen.Generate(context.Background(), GenerateRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"bad request", 400, `{"error":{"message":"organizer rejected"}}`},
		{"empty", 200, `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			gen := NewAPIGenerator(config.OrganizerConfig{Model: "model", APIKey: "key", BaseURL: srv.URL + "/v1"})
			if _, err := gen.Generate(context.Background(), GenerateRequest{User: "transcript"}); err == nil {
				t.Fatal("expected API error")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := gen.Generate(ctx, GenerateRequest{User: "transcript"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation = %v", err)
			}
		})
	}
}
