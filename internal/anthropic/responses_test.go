package anthropic

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

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func TestResponsesFormatAndEndpoint(t *testing.T) {
	for _, format := range []string{"responses", " Responses ", "openai_responses", "openai-responses"} {
		if got := NormalizeFormat(format); got != FormatResponses {
			t.Errorf("NormalizeFormat(%q) = %q", format, got)
		}
	}
	for _, base := range []string{
		"https://api.example.test", "https://api.example.test/",
		"https://api.example.test/v1", "https://api.example.test/v1/",
		"https://api.example.test/v1/responses", "https://api.example.test/v1/responses/",
	} {
		if got := responsesEndpoint(base); got != "https://api.example.test/v1/responses" {
			t.Errorf("responsesEndpoint(%q) = %q", base, got)
		}
	}
	if got := responsesEndpoint("https://api.example.test/gateway"); got != "https://api.example.test/gateway/v1/responses" {
		t.Fatalf("gateway endpoint = %q", got)
	}
}

func TestBuildResponsesRequest(t *testing.T) {
	req := MessageRequest{
		Model: "test-model", MaxTokens: 99, System: "sys",
		Thinking: true, ThinkingText: true, Effort: "high", Stream: true,
		Messages: []sdk.MessageParam{
			sdk.NewUserMessage(sdk.NewTextBlock("hi")),
			sdk.NewAssistantMessage(
				sdk.NewTextBlock("run a tool"),
				sdk.ContentBlockParamUnion{OfToolUse: &sdk.ToolUseBlockParam{
					ID: "call_1", Name: "Bash", Input: map[string]any{"command": "echo hi"},
				}},
			),
			sdk.NewUserMessage(ToolResultBlock(ToolResult{ToolUseID: "call_1", Text: "hi\n"})),
		},
		Tools: []sdk.ToolUnionParam{{OfTool: &sdk.ToolParam{
			Name: "Bash", Description: sdk.String("run"),
			InputSchema: sdk.ToolInputSchemaParam{
				Properties: map[string]any{"command": map[string]any{"type": "string"}},
				Required:   []string{"command"}, ExtraFields: map[string]any{"additionalProperties": false},
			},
		}}},
	}
	body, err := buildResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.Model != req.Model || body.Instructions != "sys" || body.MaxOutputTokens != 99 || !body.Stream || body.Store {
		t.Fatalf("request = %#v", body)
	}
	if body.Reasoning == nil || body.Reasoning.Effort != "high" || body.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning = %#v", body.Reasoning)
	}
	want := []responsesInputItem{
		{Type: "message", Role: "user", Content: []responsesContentPart{{Type: "input_text", Text: "hi"}}},
		{Type: "message", Role: "assistant", Content: []responsesContentPart{{Type: "output_text", Text: "run a tool"}}},
		{Type: "function_call", CallID: "call_1", Name: "Bash", Arguments: `{"command":"echo hi"}`},
		{Type: "function_call_output", CallID: "call_1", Output: new("hi\n")},
	}
	if !reflect.DeepEqual(body.Input, want) {
		t.Fatalf("input = %#v; want %#v", body.Input, want)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["store"] != false || wire["max_output_tokens"] != float64(99) {
		t.Fatalf("wire = %s", raw)
	}
	for _, key := range []string{"messages", "max_tokens", "thinking", "reasoning_effort", "stream_options"} {
		if _, exists := wire[key]; exists {
			t.Errorf("chat/anthropic field %s leaked into responses: %s", key, raw)
		}
	}
	tool := wire["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "Bash" || tool["strict"] != false || tool["function"] != nil {
		t.Fatalf("tool = %#v", tool)
	}
	if tool["parameters"].(map[string]any)["additionalProperties"] != false {
		t.Fatalf("schema = %#v", tool["parameters"])
	}
}

func TestBuildResponsesRequestPreservesHistoryOrder(t *testing.T) {
	body, err := buildResponsesRequest(MessageRequest{Messages: []sdk.MessageParam{
		sdk.NewUserMessage(
			sdk.NewTextBlock("before"),
			ToolResultBlock(ToolResult{ToolUseID: "call_1", Text: ""}),
			sdk.NewTextBlock("after"),
		),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Input) != 3 || body.Input[0].Content[0].Text != "before" || body.Input[2].Content[0].Text != "after" {
		t.Fatalf("input = %#v", body.Input)
	}
	raw, _ := json.Marshal(body.Input[1])
	if string(raw) != `{"type":"function_call_output","call_id":"call_1","output":""}` {
		t.Fatalf("empty tool output = %s", raw)
	}
}

func TestBuildResponsesRequestMapsImages(t *testing.T) {
	body, err := buildResponsesRequest(MessageRequest{Messages: []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("what is this?"), sdk.ContentBlockParamUnion{OfImage: &sdk.ImageBlockParam{
			Source: sdk.ImageBlockParamSourceUnion{OfURL: &sdk.URLImageSourceParam{URL: "https://example.test/image.png"}},
		}}),
		sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{OfToolUse: &sdk.ToolUseBlockParam{
			ID: "call_img", Name: "ViewImage", Input: map[string]any{"path": "image.png"},
		}}),
		sdk.NewUserMessage(ToolResultBlock(ToolResult{
			ToolUseID: "call_img", Text: "Image: image.png", ImageMimeType: "image/png", ImageData: "aGVsbG8=",
		})),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Input) != 4 {
		t.Fatalf("input = %#v", body.Input)
	}
	if body.Input[0].Content[1].Type != "input_image" || body.Input[0].Content[1].ImageURL != "https://example.test/image.png" {
		t.Fatalf("attachment = %#v", body.Input[0])
	}
	if body.Input[2].Type != "function_call_output" || *body.Input[2].Output != "Image: image.png" {
		t.Fatalf("tool output = %#v", body.Input[2])
	}
	image := body.Input[3].Content[1]
	if image.Type != "input_image" || image.ImageURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("tool image = %#v", image)
	}
}

func TestBuildResponsesRequestDefaultsAndErrors(t *testing.T) {
	body, err := buildResponsesRequest(MessageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if body.Model != DefaultModel || body.MaxOutputTokens != 16_000 || body.Reasoning != nil || body.Input == nil {
		t.Fatalf("defaults = %#v", body)
	}
	for _, message := range []sdk.MessageParam{
		{Role: "invalid"},
		sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{OfToolUse: &sdk.ToolUseBlockParam{Input: make(chan int)}}),
		sdk.NewUserMessage(sdk.ContentBlockParamUnion{OfImage: &sdk.ImageBlockParam{}}),
	} {
		if _, err := buildResponsesRequest(MessageRequest{Messages: []sdk.MessageParam{message}}); err == nil {
			t.Errorf("expected error for %#v", message)
		}
	}
}

const responsesTestOutput = `[
	{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"opaque-encrypted-content"},
	{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
	{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Bash","arguments":"{\"command\":\"echo hi\"}"}
]`

func TestResponsesClientCreateNonStream(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/v1/responses/"} {
		t.Run(suffix, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("request: %s %s; headers = %#v", r.Method, r.URL.Path, r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["stream"] != false || body["store"] != false || body["model"] != "test-model" {
					t.Errorf("body = %#v", body)
				}
				fmt.Fprintf(w, `{"id":"resp_1","model":"test-model","status":"completed","output":%s,"usage":{"input_tokens":100,"output_tokens":12,"input_tokens_details":{"cached_tokens":40}}}`, responsesTestOutput)
			}))
			defer server.Close()
			client := NewClient(Options{APIKey: "test-key", BaseURL: server.URL + suffix, Format: "responses"})
			msg, err := client.Create(t.Context(), MessageRequest{Model: "test-model", Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hi"))}})
			if err != nil {
				t.Fatal(err)
			}
			if msg.ID != "resp_1" || string(msg.Model) != "test-model" || TextFromMessage(msg) != "hello" || msg.StopReason != sdk.StopReasonToolUse {
				t.Fatalf("message = %#v", msg)
			}
			if len(msg.Content) != 3 || msg.Content[0].Thinking != "plan" {
				t.Fatalf("content = %#v", msg.Content)
			}
			tools := ToolUseBlocks(msg)
			if len(tools) != 1 || tools[0].ID != "call_1" || tools[0].Name != "Bash" {
				t.Fatalf("tools = %#v", tools)
			}
			param := msg.ToParam()
			tool := param.Content[2].OfToolUse
			if tool == nil || tool.ID != "call_1" || tool.Name != "Bash" {
				t.Fatalf("tool history = %#v", param)
			}
			if msg.Usage.InputTokens != 60 || msg.Usage.OutputTokens != 12 || msg.Usage.CacheReadInputTokens != 40 {
				t.Fatalf("usage = %#v", msg.Usage)
			}
		})
	}
}

func TestResponsesStopReasonsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		stop sdk.StopReason
		err  string
	}{
		{"completed", `{"status":"completed","output":[]}`, sdk.StopReasonEndTurn, ""},
		{"length", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, sdk.StopReasonMaxTokens, ""},
		{"filter", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, sdk.StopReasonRefusal, ""},
		{"refusal", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}`, sdk.StopReasonRefusal, ""},
		{"failed", `{"status":"failed","error":{"message":"bad model"}}`, "", "bad model"},
		{"pending", `{"status":"in_progress"}`, "", "unexpected status"},
		{"missing reason", `{"status":"incomplete"}`, "", "without a reason"},
		{"unknown reason", `{"status":"incomplete","incomplete_details":{"reason":"other"}}`, "", "incomplete: other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response responsesResponse
			if err := json.Unmarshal([]byte(tc.body), &response); err != nil {
				t.Fatal(err)
			}
			msg, err := responsesToMessage(response)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || msg.StopReason != tc.stop {
				t.Fatalf("message = %#v; error = %v", msg, err)
			}
		})
	}
}

func TestResponsesClientCreateStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request = %s; headers = %#v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"response.created","response":{"id":"resp_1","model":"test-model","status":"in_progress"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"plan"}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
			`{"type":"response.content_part.added","output_index":1,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"hel"}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"lo"}`,
			`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Bash","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"command\":"}`,
			`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"\"echo hi\"}"}`,
			fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":%s,"usage":{"input_tokens":20,"output_tokens":5,"input_tokens_details":{"cached_tokens":12}}}}`, responsesTestOutput),
		}
		for _, event := range events {
			fmt.Fprint(w, "event: ignored\n")
			for _, line := range strings.Split(event, "\n") {
				fmt.Fprintf(w, "data: %s\n", line)
			}
			fmt.Fprint(w, "\n")
		}
	}))
	defer server.Close()
	var text, thinking strings.Builder
	msg, err := NewClient(Options{BaseURL: server.URL, Format: "responses"}).Create(t.Context(), MessageRequest{
		Stream: true, OnTextDelta: func(s string) { text.WriteString(s) }, OnThinkingDelta: func(s string) { thinking.WriteString(s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "hello" || thinking.String() != "plan" || TextFromMessage(msg) != "hello" {
		t.Fatalf("deltas = %q/%q; message = %#v", text.String(), thinking.String(), msg)
	}
	if msg.Content[0].Thinking != "plan" || !strings.HasPrefix(msg.Content[0].Signature, responsesReasoningSignaturePrefix) || len(ToolUseBlocks(msg)) != 1 || msg.StopReason != sdk.StopReasonToolUse {
		t.Fatalf("content = %#v", msg.Content)
	}
	if msg.Usage.InputTokens != 8 || msg.Usage.CacheReadInputTokens != 12 || msg.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %#v", msg.Usage)
	}
}

func TestResponsesStreamAccumulationAndSSEFraming(t *testing.T) {
	stream := strings.Join([]string{
		": comment\r\n\r\n",
		"event: response.output_item.added\r\ndata: {\"output_index\":1,\n" +
			"data: \"item\":{\"type\":\"function_call\",\"call_id\":\"call_b\",\"name\":\"Bash\",\"arguments\":\"\"}}\r\n\r\n",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_a","name":"View","arguments":""}}` + "\n\n",
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{}"}` + "\n\n",
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"a\"}"}` + "\n\n",
		`data: {"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"command\":\"ls\"}"}` + "\n\n",
		// Final event has no newline; flush at EOF and use accumulated output.
		`event: response.completed` + "\n" + `data: {"response":{"id":"resp_1","model":"m","status":"completed"}}`,
	}, "")
	msg, err := consumeResponsesStream(strings.NewReader(stream), MessageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	tools := ToolUseBlocks(msg)
	if len(tools) != 2 || tools[0].ID != "call_a" || tools[1].ID != "call_b" {
		t.Fatalf("tools = %#v", tools)
	}
	var input map[string]any
	if err := json.Unmarshal(tools[1].Input, &input); err != nil || input["command"] != "ls" {
		t.Fatalf("input = %s, error = %v", tools[1].Input, err)
	}
}

func TestResponsesStreamErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"", "before a terminal event"},
		{"data: [DONE]\n\n", "before a terminal event"},
		{"data: invalid\n\n", "decode responses stream event"},
		{`data: {"type":"error","message":"overloaded"}` + "\n\n", "overloaded"},
		{`data: {"type":"response.failed","response":{"error":{"message":"failed model"}}}` + "\n\n", "failed model"},
		{`data: {"type":"response.completed"}` + "\n\n", "missing response"},
		{`data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n", "unknown output item"},
		{`data: {"type":"response.function_call_arguments.delta","delta":"{}"}` + "\n\n", "unknown output item"},
	} {
		if _, err := consumeResponsesStream(strings.NewReader(tc.body), MessageRequest{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("stream %q: error = %v, want %q", tc.body, err, tc.want)
		}
	}
}

func TestResponsesStreamReturnsAtTerminalEvent(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() {
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
		// Deliberately leave the connection open after completion.
	}()
	msg, err := consumeResponsesStream(reader, MessageRequest{})
	if err != nil || msg.StopReason != sdk.StopReasonEndTurn {
		t.Fatalf("message = %#v; error = %v", msg, err)
	}
}

func TestResponsesClientRetriesAndHTTPErrors(t *testing.T) {
	previous := openAIRetryDelay
	openAIRetryDelay = 0
	t.Cleanup(func() { openAIRetryDelay = previous })
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusBadRequest} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if status == http.StatusBadRequest || attempts < 3 {
					http.Error(w, "bad request", status)
					return
				}
				fmt.Fprint(w, `{"status":"completed","output":[]}`)
			}))
			defer server.Close()
			_, err := NewClient(Options{BaseURL: server.URL, Format: "responses"}).Create(t.Context(), MessageRequest{})
			if status == http.StatusBadRequest {
				if err == nil || !strings.Contains(err.Error(), "responses 400") || attempts != 1 {
					t.Fatalf("error = %v; attempts = %d", err, attempts)
				}
			} else if err != nil || attempts != 3 {
				t.Fatalf("error = %v; attempts = %d", err, attempts)
			}
		})
	}
}

func TestResponsesClientRequiresBaseURLAndHonorsCancellation(t *testing.T) {
	if _, err := NewClient(Options{Format: "responses"}).Create(t.Context(), MessageRequest{}); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("missing base URL: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewClient(Options{BaseURL: "http://127.0.0.1:1", Format: "responses"}).Create(ctx, MessageRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestResponsesTextPartsKeepRequiredFields(t *testing.T) {
	for _, tc := range []struct {
		part responsesContentPart
		want string
	}{
		{responsesContentPart{Type: "input_text"}, `{"type":"input_text","text":""}`},
		{responsesContentPart{Type: "output_text"}, `{"type":"output_text","text":""}`},
		{responsesContentPart{Type: "input_image", ImageURL: "https://example.test/image.png"}, `{"type":"input_image","image_url":"https://example.test/image.png"}`},
	} {
		raw, err := json.Marshal(tc.part)
		if err != nil || string(raw) != tc.want {
			t.Fatalf("part = %s; error = %v, want %s", raw, err, tc.want)
		}
	}
}

func TestResponsesStreamTextAndReasoningFallback(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning"}}`,
		`{"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"think"}`,
		`{"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"thinking"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message"}}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"first"}`,
		`{"type":"response.content_part.done","output_index":1,"content_index":0,"part":{"type":"output_text","text":"first"}}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":1,"delta":"second"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}, "\n\ndata: ")
	msg, err := consumeResponsesStream(strings.NewReader("data: "+stream+"\n\n"), MessageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Content) != 3 || msg.Content[0].Thinking != "thinking" || msg.Content[1].Text != "first" || msg.Content[2].Text != "second" {
		t.Fatalf("content = %#v", msg.Content)
	}
}

func TestResponsesStreamIncompleteAndRefusal(t *testing.T) {
	msg, err := consumeResponsesStream(strings.NewReader(`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}}`+"\n\n"), MessageRequest{})
	if err != nil || msg.StopReason != sdk.StopReasonMaxTokens || TextFromMessage(msg) != "partial" {
		t.Fatalf("incomplete message = %#v; error = %v", msg, err)
	}
	stream := "data: " + strings.Join([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}`,
		`{"type":"response.refusal.delta","output_index":0,"content_index":0,"delta":"cannot"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"refusal","refusal":"cannot comply"}]}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}, "\n\ndata: ") + "\n\n"
	var text strings.Builder
	msg, err = consumeResponsesStream(strings.NewReader(stream), MessageRequest{OnTextDelta: func(s string) { text.WriteString(s) }})
	if err != nil || msg.StopReason != sdk.StopReasonRefusal || TextFromMessage(msg) != "cannot comply" || text.String() != "cannot" {
		t.Fatalf("refusal message = %#v; error = %v; delta = %q", msg, err, text.String())
	}
	// Receiving valid deltas before EOF still does not make a response complete.
	stream = "data: " + strings.Join([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}`,
	}, "\n\ndata: ") + "\n\n"
	if _, err := consumeResponsesStream(strings.NewReader(stream), MessageRequest{}); err == nil || !strings.Contains(err.Error(), "before a terminal event") {
		t.Fatalf("interrupted stream: %v", err)
	}
}

func TestResponsesEncryptedReasoningSurvivesHistoryRoundTrip(t *testing.T) {
	for _, summaryText := range []string{"", "plan"} {
		t.Run(summaryText, func(t *testing.T) {
			item := responsesOutputItem{ID: "rs_1", Type: "reasoning", EncryptedContent: "opaque-encrypted-content"}
			if summaryText != "" {
				item.Summary = []responsesContentPart{{Type: "summary_text", Text: summaryText}}
			}
			msg, err := responsesToMessage(responsesResponse{
				ID: "resp_1", Status: "completed", Model: "test-model",
				Output: []responsesOutputItem{item, {
					Type: "function_call", CallID: "call_1", Name: "Bash", Arguments: `{}`,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(msg.Content) != 2 || msg.Content[0].Thinking != summaryText || !strings.HasPrefix(msg.Content[0].Signature, responsesReasoningSignaturePrefix) {
				t.Fatalf("reasoning block = %#v", msg.Content)
			}
			// Session history stores SDK params as JSON, not the wire response.
			saved, err := json.Marshal(msg.ToParam())
			if err != nil {
				t.Fatal(err)
			}
			var history sdk.MessageParam
			if err := json.Unmarshal(saved, &history); err != nil {
				t.Fatal(err)
			}
			body, err := buildResponsesRequest(MessageRequest{Messages: []sdk.MessageParam{
				history, ToolResultMessage("call_1", "done", false),
			}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body.Include, []string{"reasoning.encrypted_content"}) {
				t.Fatalf("include = %#v", body.Include)
			}
			if len(body.Input) != 3 || body.Input[0].Type != "reasoning" || body.Input[1].Type != "function_call" || body.Input[2].Type != "function_call_output" {
				t.Fatalf("input = %#v", body.Input)
			}
			reasoning := body.Input[0]
			if reasoning.ID != "rs_1" || reasoning.EncryptedContent != item.EncryptedContent || reasoning.Summary == nil {
				t.Fatalf("reasoning = %#v", reasoning)
			}
			if summaryText == "" {
				raw, _ := json.Marshal(reasoning)
				if !strings.Contains(string(raw), `"summary":[]`) {
					t.Fatalf("empty summary must be an array: %s", raw)
				}
			} else if len(*reasoning.Summary) != 1 || (*reasoning.Summary)[0].Text != summaryText {
				t.Fatalf("summary = %#v", reasoning.Summary)
			}
		})
	}
}

func TestResponsesDoesNotReplayNativeThinking(t *testing.T) {
	body, err := buildResponsesRequest(MessageRequest{Messages: []sdk.MessageParam{
		sdk.NewAssistantMessage(
			sdk.ContentBlockParamUnion{OfThinking: &sdk.ThinkingBlockParam{Thinking: "native", Signature: "anthropic-signature"}},
			sdk.NewTextBlock("answer"),
		),
	}})
	if err != nil || len(body.Input) != 1 || body.Input[0].Content[0].Text != "answer" {
		t.Fatalf("body = %#v; error = %v", body, err)
	}
	for _, signature := range []string{responsesReasoningSignaturePrefix + "invalid", responsesReasoningSignaturePrefix + `{"type":"message"}`} {
		_, err := buildResponsesRequest(MessageRequest{Messages: []sdk.MessageParam{
			sdk.NewAssistantMessage(sdk.ContentBlockParamUnion{OfThinking: &sdk.ThinkingBlockParam{Signature: signature}}),
		}})
		if err == nil {
			t.Errorf("expected invalid reasoning history error for %q", signature)
		}
	}
}
