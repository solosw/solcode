package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
)

type compressionRequestClientFunc func(context.Context, cpanthropic.MessageRequest) (*sdk.Message, error)

func (f compressionRequestClientFunc) Create(ctx context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
	return f(ctx, req)
}

func TestForcedCompressionStreamsToolProgressAcrossProtocols(t *testing.T) {
	for _, format := range []string{"anthropic", "openai", "responses"} {
		t.Run(format, func(t *testing.T) {
			progressSeen := make(chan struct{}, 1)
			args := `{"start":"m2","end":"m4","summary":"Previous work completed; continue active task.","revision":0}`
			emit := func(w http.ResponseWriter, v any) { raw, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", raw) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["stream"] != true || body["tool_choice"] == nil {
					t.Error("forced compression lost streaming or tool choice")
				}
				if body["max_tokens"] != nil && body["max_tokens"].(float64) > 8192 {
					t.Error("uncapped output")
				}
				if body["max_output_tokens"] != nil && body["max_output_tokens"].(float64) > 8192 {
					t.Error("uncapped output")
				}
				if body["thinking"] != nil || body["reasoning"] != nil {
					t.Error("compression enabled thinking")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch format {
				case "anthropic":
					emit(w, map[string]any{"type": "message_start", "message": map[string]any{"id": "fold", "type": "message", "role": "assistant", "model": "chat", "content": []any{}, "usage": map[string]any{"input_tokens": 190000, "output_tokens": 0}}})
					emit(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "fold", "name": "compress", "input": map[string]any{}}})
					emit(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}})
				case "openai":
					emit(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "fold", "type": "function", "function": map[string]any{"name": "compress", "arguments": args}}}}}}})
				case "responses":
					emit(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "item_fold", "call_id": "fold", "name": "compress", "arguments": ""}})
					emit(w, map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": args})
				}
				w.(http.Flusher).Flush()
				// Completion is withheld until tool arguments have reached the UI status.
				select {
				case <-progressSeen:
				case <-r.Context().Done():
					return
				}
				switch format {
				case "anthropic":
					emit(w, map[string]any{"type": "content_block_stop", "index": 0})
					emit(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 50}})
					emit(w, map[string]any{"type": "message_stop"})
				case "openai":
					emit(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}})
					fmt.Fprint(w, "data: [DONE]\n\n")
				case "responses":
					emit(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": "fold", "name": "compress", "arguments": args}}}})
				}
			}))
			defer server.Close()
			eng := NewEngine(Config{Client: cpanthropic.NewClient(cpanthropic.Options{BaseURL: server.URL, Format: format}), MaxContextTokens: 200000, OnStatus: func(status string) {
				if strings.Contains(status, "receiving summary") {
					select {
					case progressSeen <- struct{}{}:
					default:
					}
				}
			}})
			req := cpanthropic.MessageRequest{Model: "chat", MaxTokens: 64000, Stream: true, ForceTool: "compress", Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("compress"))}, Tools: []sdk.ToolUnionParam{{OfTool: &sdk.ToolParam{Name: "compress", InputSchema: sdk.ToolInputSchemaParam{Properties: map[string]any{}}}}}, OnTextDelta: func(string) { t.Error("summary leaked into chat") }}
			message, err := eng.createForcedCompression(t.Context(), req, 195000, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			uses := cpanthropic.ToolUseBlocks(message)
			if message.StopReason != sdk.StopReasonToolUse || len(uses) != 1 || uses[0].Name != "compress" {
				t.Fatalf("invalid streamed compress stop reason: %s", message.StopReason)
			}
			var got, want map[string]any
			if err := json.Unmarshal(uses[0].Input, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(args), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("streamed arguments changed")
			}
		})
	}
}

func TestForcedCompressionTimeoutCancelsUnresponsiveProvider(t *testing.T) {
	for _, format := range []string{"anthropic", "openai", "responses"} {
		t.Run(format, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
			}))
			defer server.Close()
			eng := NewEngine(Config{Client: cpanthropic.NewClient(cpanthropic.Options{BaseURL: server.URL, Format: format}), MaxContextTokens: 200000})
			started := time.Now()
			_, err := eng.createForcedCompression(t.Context(), cpanthropic.MessageRequest{Model: "chat", ForceTool: "compress", Stream: true}, 180000, 100*time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "history is preserved") || time.Since(started) > time.Second {
				t.Fatalf("timeout not bounded: %v", err)
			}
		})
	}
}

func TestForcedCompressionReservesOutputHeadroom(t *testing.T) {
	for _, tc := range []struct {
		name             string
		input, max, want int64
		stream           bool
	}{
		{"normal", 170000, 64000, 8192, true},
		{"near full", 195000, 64000, 3976, true},
		{"lower configured limit", 195000, 2000, 2000, false},
		{"no headroom", 199000, 64000, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			eng := NewEngine(Config{MaxContextTokens: 200000, Client: compressionRequestClientFunc(func(ctx context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				called = true
				if req.MaxTokens != tc.want || req.Stream != tc.stream {
					t.Fatalf("max=%d stream=%v", req.MaxTokens, req.Stream)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("no compression deadline")
				}
				return &sdk.Message{}, nil
			})})
			_, err := eng.createForcedCompression(t.Context(), cpanthropic.MessageRequest{MaxTokens: tc.max, Stream: tc.stream}, tc.input, time.Second)
			if tc.want == 0 {
				if err == nil || called {
					t.Fatal("over-budget request sent")
				}
			} else if err != nil || !called {
				t.Fatal(err)
			}
		})
	}
}

func TestForcedCompressionPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	eng := NewEngine(Config{Client: compressionRequestClientFunc(func(ctx context.Context, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	})})
	_, err := eng.createForcedCompression(ctx, cpanthropic.MessageRequest{}, 0, time.Second)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
}
