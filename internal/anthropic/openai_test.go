package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func TestOpenAIRequestRetriesRetryableStatus(t *testing.T) {
	previousDelay := openAIRetryDelay
	openAIRetryDelay = 0
	t.Cleanup(func() { openAIRetryDelay = previousDelay })

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.UserAgent() != "solcode/1.0" {
			t.Errorf("User-Agent = %q", r.UserAgent())
		}
		if attempts <= 2 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp, err := doOpenAIRequest(context.Background(), server.Client(), server.URL, []byte(`{"model":"test"}`), "key", false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || attempts != 3 {
		t.Fatalf("status = %d, attempts = %d; want 200 and 3", resp.StatusCode, attempts)
	}
}

func TestOpenAIRequestDoesNotRetryNonRetryableStatus(t *testing.T) {
	previousDelay := openAIRetryDelay
	openAIRetryDelay = 0
	t.Cleanup(func() { openAIRetryDelay = previousDelay })

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "invalid request", http.StatusBadRequest)
	}))
	defer server.Close()

	resp, err := doOpenAIRequest(context.Background(), server.Client(), server.URL, []byte(`{"model":"test"}`), "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || attempts != 1 {
		t.Fatalf("status = %d, attempts = %d; want 400 and 1", resp.StatusCode, attempts)
	}
}

func TestOpenAIRequestStopsRetryingWhenCancelled(t *testing.T) {
	previousDelay := openAIRetryDelay
	openAIRetryDelay = time.Hour
	t.Cleanup(func() { openAIRetryDelay = previousDelay })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		cancel()
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := doOpenAIRequest(ctx, server.Client(), server.URL, []byte(`{"model":"test"}`), "", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestNormalizeFormat(t *testing.T) {
	if got := NormalizeFormat(""); got != FormatAnthropic {
		t.Fatalf("empty = %q", got)
	}
	if got := NormalizeFormat("OpenAI"); got != FormatOpenAI {
		t.Fatalf("OpenAI = %q", got)
	}
	if got := NormalizeFormat("chat_completions"); got != FormatOpenAI {
		t.Fatalf("chat_completions = %q", got)
	}
}

func TestBuildOpenAIRequestMapsSystemToolsAndHistory(t *testing.T) {
	req := MessageRequest{
		Model:     "gpt-test",
		MaxTokens: 99,
		System:    "sys",
		Messages: []sdk.MessageParam{
			sdk.NewUserMessage(sdk.NewTextBlock("hi")),
			sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{
				OfToolUse: &sdk.ToolUseBlockParam{
					ID:    "call_1",
					Name:  "Bash",
					Input: map[string]any{"command": "echo hi"},
				},
			}),
			sdk.NewUserMessage(ToolResultBlock(ToolResult{ToolUseID: "call_1", Text: "hi\n"})),
		},
		Tools: []sdk.ToolUnionParam{
			{OfTool: &sdk.ToolParam{
				Name:        "Bash",
				Description: sdk.String("run"),
				InputSchema: sdk.ToolInputSchemaParam{Properties: map[string]any{"command": map[string]any{"type": "string"}}, Required: []string{"command"}},
			}},
		},
		Thinking:     true,
		ThinkingText: true,
		Effort:       "high",
	}
	body, err := buildOpenAIRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.Model != "gpt-test" || body.MaxTokens != 99 {
		t.Fatalf("model/max = %s/%d", body.Model, body.MaxTokens)
	}
	if len(body.Tools) != 1 || body.Tools[0].Function.Name != "Bash" {
		t.Fatalf("tools = %#v", body.Tools)
	}
	if len(body.Messages) < 4 {
		t.Fatalf("messages = %#v", body.Messages)
	}
	if body.Messages[0].Role != "system" || body.Messages[0].Content != "sys" {
		t.Fatalf("system = %#v", body.Messages[0])
	}
	foundToolCall := false
	foundToolResult := false
	for _, m := range body.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "call_1" {
			foundToolCall = true
		}
		if m.Role == "tool" && m.ToolCallID == "call_1" {
			foundToolResult = true
		}
	}
	if !foundToolCall || !foundToolResult {
		t.Fatalf("missing tool call/result mapping: %#v", body.Messages)
	}
	if body.Thinking == nil || body.Thinking.Type != "adaptive" {
		t.Fatalf("thinking = %#v", body.Thinking)
	}
	if body.Thinking.Display != "summarized" {
		t.Fatalf("thinking.display = %q, want summarized", body.Thinking.Display)
	}
	if body.ReasoningEffort != "high" {
		t.Fatalf("effort = %q", body.ReasoningEffort)
	}
}

func TestOpenAIClientCreateNonStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		var body openAIChatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		if body.Messages[0].Role != "system" {
			t.Fatalf("expected system first, got %#v", body.Messages[0])
		}
		_ = json.NewEncoder(w).Encode(openAIChatResponse{
			Model: body.Model,
			Choices: []openAIChatChoice{{
				Message: openAIChatMessage{
					Role:    "assistant",
					Content: "hello",
					ToolCalls: []openAIToolCall{{
						ID:   "call_9",
						Type: "function",
						Function: openAIFunctionCall{
							Name:      "Bash",
							Arguments: `{"command":"pwd"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
			Usage: &openAIUsage{
				PromptTokens:             100,
				CompletionTokens:         10,
				CacheReadInputTokens:     40,
				CacheCreationInputTokens: 5,
			},
		})
	}))
	defer server.Close()

	client := NewClient(Options{APIKey: "test-key", BaseURL: server.URL, Format: FormatOpenAI})
	msg, err := client.Create(t.Context(), MessageRequest{
		Model:     "m",
		MaxTokens: 16,
		System:    "sys",
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hi"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != sdk.StopReasonToolUse {
		t.Fatalf("stop = %s", msg.StopReason)
	}
	if TextFromMessage(msg) != "hello" {
		t.Fatalf("text = %q", TextFromMessage(msg))
	}
	tools := ToolUseBlocks(msg)
	if len(tools) != 1 || tools[0].Name != "Bash" || tools[0].ID != "call_9" {
		t.Fatalf("tools = %#v", tools)
	}
	if msg.Usage.CacheReadInputTokens != 40 || msg.Usage.CacheCreationInputTokens != 5 {
		t.Fatalf("usage = %#v", msg.Usage)
	}
	if msg.Usage.InputTokens != 60 {
		t.Fatalf("input tokens = %d, want 60", msg.Usage.InputTokens)
	}
	param := msg.ToParam()
	if len(param.Content) == 0 {
		t.Fatal("ToParam produced empty content")
	}
}

func TestOpenAIClientCreateStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"reasoning_content":"plan "}}]}`,
			`{"choices":[{"delta":{"reasoning":"step","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":12}}}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var got, gotThinking strings.Builder
	client := NewClient(Options{BaseURL: server.URL, Format: "openai"})
	msg, err := client.Create(t.Context(), MessageRequest{
		Model:           "m",
		MaxTokens:       16,
		Messages:        []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hi"))},
		Thinking:        true,
		ThinkingText:    true,
		Stream:          true,
		OnTextDelta:     func(s string) { got.WriteString(s) },
		OnThinkingDelta: func(s string) { gotThinking.WriteString(s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "Hello" {
		t.Fatalf("text deltas = %q", got.String())
	}
	if gotThinking.String() != "plan step" {
		t.Fatalf("thinking deltas = %q", gotThinking.String())
	}
	if TextFromMessage(msg) != "Hello" {
		t.Fatalf("text = %q", TextFromMessage(msg))
	}
	var thought string
	for _, block := range msg.Content {
		if block.Type == "thinking" {
			thought = block.Thinking
		}
	}
	if thought != "plan step" {
		t.Fatalf("thinking content = %q", thought)
	}
	if msg.Usage.CacheReadInputTokens != 12 {
		t.Fatalf("cache read = %d", msg.Usage.CacheReadInputTokens)
	}
}

func TestBuildOpenAIRequestMapsViewImageToolResult(t *testing.T) {
	// Tiny 1x1 PNG base64
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	req := MessageRequest{
		Model:     "gpt-test",
		MaxTokens: 16,
		Messages: []sdk.MessageParam{
			sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{
				OfToolUse: &sdk.ToolUseBlockParam{
					ID:    "call_img",
					Name:  "ViewImage",
					Input: map[string]any{"path": "shot.png"},
				},
			}),
			sdk.NewUserMessage(ToolResultBlock(ToolResult{
				ToolUseID:     "call_img",
				Text:          "Image: shot.png",
				ImageMimeType: "image/png",
				ImageData:     png,
			})),
		},
	}
	body, err := buildOpenAIRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var sawTool, sawImageUser bool
	for _, m := range body.Messages {
		if m.Role == "tool" && m.ToolCallID == "call_img" {
			sawTool = true
			if !strings.Contains(fmt.Sprint(m.Content), "Image: shot.png") {
				t.Fatalf("tool content missing caption: %#v", m.Content)
			}
		}
		if m.Role == "user" {
			parts, ok := m.Content.([]openAIContentPart)
			if !ok {
				continue
			}
			for _, part := range parts {
				if part.Type == "image_url" && part.ImageURL != nil && strings.HasPrefix(part.ImageURL.URL, "data:image/png;base64,") {
					sawImageUser = true
				}
			}
		}
	}
	if !sawTool {
		t.Fatalf("missing tool message: %#v", body.Messages)
	}
	if !sawImageUser {
		t.Fatalf("ViewImage pixels were not mapped to a user image_url message: %#v", body.Messages)
	}
}

func TestBuildOpenAIRequestMapsAtImageAttachment(t *testing.T) {
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	req := MessageRequest{
		Model:     "gpt-test",
		MaxTokens: 16,
		Messages: []sdk.MessageParam{
			sdk.NewUserMessage(
				sdk.NewTextBlock("what is in this image?"),
				sdk.ContentBlockParamUnion{OfImage: &sdk.ImageBlockParam{
					Source: sdk.ImageBlockParamSourceUnion{OfBase64: &sdk.Base64ImageSourceParam{
						MediaType: "image/png",
						Data:      png,
					}},
				}},
			),
		},
	}
	body, err := buildOpenAIRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		t.Fatalf("messages = %#v", body.Messages)
	}
	parts, ok := body.Messages[0].Content.([]openAIContentPart)
	if !ok {
		t.Fatalf("expected multimodal parts, got %#v", body.Messages[0].Content)
	}
	var hasText, hasImage bool
	for _, part := range parts {
		if part.Type == "text" && strings.Contains(part.Text, "what is in this image") {
			hasText = true
		}
		if part.Type == "image_url" && part.ImageURL != nil && strings.Contains(part.ImageURL.URL, "base64,") {
			hasImage = true
		}
	}
	if !hasText || !hasImage {
		t.Fatalf("missing text/image parts: %#v", parts)
	}
}
