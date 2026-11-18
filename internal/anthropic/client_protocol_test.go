package anthropic

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func TestAnthropicClientUsesHandwrittenMessagesProtocol(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	var gotVersion string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		if r.UserAgent() != "solcode/1.0" {
			t.Errorf("User-Agent = %q", r.UserAgent())
		}
		gotVersion = r.Header.Get("anthropic-version")
		data := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(data)
		gotBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"output_tokens":1}}`))
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "test-key", BaseURL: server.URL, Format: FormatAnthropic})
	message, err := client.Create(t.Context(), MessageRequest{
		Model:     "claude-test",
		MaxTokens: 128,
		System:    "system prompt",
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAPIKey != "test-key" || gotVersion != "2023-06-01" {
		t.Fatalf("headers x-api-key=%q anthropic-version=%q", gotAPIKey, gotVersion)
	}
	if !strings.Contains(gotBody, `"model":"claude-test"`) || !strings.Contains(gotBody, `"cache_control"`) {
		t.Fatalf("unexpected request body: %s", gotBody)
	}
	if TextFromMessage(message) != "ok" {
		t.Fatalf("response text = %q", TextFromMessage(message))
	}
}

func TestAnthropicClientRetriesRetryableStatus(t *testing.T) {
	originalDelay := anthropicRetryDelay
	anthropicRetryDelay = 0
	t.Cleanup(func() { anthropicRetryDelay = originalDelay })

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"output_tokens":1}}`))
	}))
	defer server.Close()

	message, err := NewClient(Options{BaseURL: server.URL}).Create(t.Context(), MessageRequest{
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if attempts != 3 || TextFromMessage(message) != "ok" {
		t.Fatalf("attempts=%d text=%q", attempts, TextFromMessage(message))
	}
}

func TestAnthropicClientDoesNotRetryBadRequest(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	_, err := NewClient(Options{BaseURL: server.URL}).Create(t.Context(), MessageRequest{
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))},
	})
	if err == nil {
		t.Fatal("Create succeeded for 400 response")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestAnthropicClientStreamsWithCallbacks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	var deltas strings.Builder
	client := NewClient(Options{BaseURL: server.URL, Format: FormatAnthropic})
	message, err := client.Create(t.Context(), MessageRequest{
		Messages:    []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))},
		Stream:      true,
		OnTextDelta: func(delta string) { deltas.WriteString(delta) },
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if deltas.String() != "hello" || TextFromMessage(message) != "hello" {
		t.Fatalf("deltas=%q text=%q", deltas.String(), TextFromMessage(message))
	}
}
