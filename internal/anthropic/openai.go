package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// OpenAI chat/completions request/response types used for the compatible
// transport. Only fields we emit or consume are modeled.

type openAIChatRequest struct {
	Model           string              `json:"model"`
	Messages        []openAIChatMessage `json:"messages"`
	Tools           []openAITool        `json:"tools,omitempty"`
	MaxTokens       int64               `json:"max_tokens,omitempty"`
	Stream          bool                `json:"stream,omitempty"`
	StreamOptions   *openAIStreamOpts   `json:"stream_options,omitempty"`
	ReasoningEffort string              `json:"reasoning_effort,omitempty"`
	// Extra passthrough for gateways that accept Anthropic-ish extensions.
	Thinking *openAIThinking `json:"thinking,omitempty"`
}

type openAIStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIThinking struct {
	Type    string `json:"type"`
	Display string `json:"display,omitempty"`
}

type openAIChatMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content,omitempty"` // string | []openAIContentPart
	ReasoningContent any              `json:"reasoning_content,omitempty"`
	Reasoning        any              `json:"reasoning,omitempty"`
	Thinking         any              `json:"thinking,omitempty"`
	Name             string           `json:"name,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
	Index    *int               `json:"index,omitempty"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Choices []openAIChatChoice `json:"choices"`
	Usage   *openAIUsage       `json:"usage,omitempty"`
	Error   *openAIError       `json:"error,omitempty"`
}

type openAIChatChoice struct {
	Index        int               `json:"index"`
	Message      openAIChatMessage `json:"message"`
	Delta        openAIChatMessage `json:"delta"`
	FinishReason string            `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	// Common gateway extensions for Anthropic cache accounting.
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	PromptTokensDetails      *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func (c *Client) createOpenAI(ctx context.Context, req MessageRequest) (*sdk.Message, error) {
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, fmt.Errorf("openai format requires base_url (chat/completions endpoint host)")
	}
	body, err := buildOpenAIRequest(req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}
	endpoint := c.baseURL + "/v1/chat/completions"
	client := c.http
	if client == nil {
		client = http.DefaultClient
	}
	if !req.Stream {
		client = c.idleHTTPClient()
	}
	resp, err := doOpenAIRequest(ctx, client, endpoint, raw, c.apiKey, req.Stream)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("openai chat/completions %s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}
	if req.Stream {
		return c.consumeOpenAIStream(resp.Body, req)
	}
	return decodeOpenAIResponse(resp.Body)
}

const openAIMaxRetries = 5

var openAIRetryDelay = 5 * time.Second

func doOpenAIRequest(ctx context.Context, client *http.Client, endpoint string, body []byte, apiKey string, stream bool) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= openAIMaxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
		if stream {
			httpReq.Header.Set("Accept", "text/event-stream")
		}

		resp, err := client.Do(httpReq)
		if err == nil && !isRetryableOpenAIStatus(resp.StatusCode) {
			return resp, nil
		}
		if err == nil {
			lastErr = openAIResponseError(resp)
			_ = resp.Body.Close()
		} else {
			lastErr = err
		}
		if attempt == openAIMaxRetries || ctx.Err() != nil {
			break
		}
		if err := waitOpenAIRetry(ctx, openAIRetryDelay); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("openai API failed after %d retries: %w", openAIMaxRetries, lastErr)
}

func isRetryableOpenAIStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func openAIResponseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return fmt.Errorf("openai API %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func waitOpenAIRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func buildOpenAIRequest(req MessageRequest) (openAIChatRequest, error) {
	model := req.Model
	if model == "" {
		model = DefaultModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16_000
	}
	messages, err := toOpenAIMessages(req.System, req.Messages)
	if err != nil {
		return openAIChatRequest{}, err
	}
	out := openAIChatRequest{
		Model:     model,
		Messages:  messages,
		Tools:     toOpenAITools(req.Tools),
		MaxTokens: maxTokens,
		Stream:    req.Stream,
	}
	if req.Stream {
		out.StreamOptions = &openAIStreamOpts{IncludeUsage: true}
	}
	if req.Effort != "" {
		// Many OpenAI-compatible Claude gateways map reasoning_effort from Anthropic effort.
		out.ReasoningEffort = req.Effort
	}
	if req.Thinking {
		out.Thinking = &openAIThinking{Type: "adaptive"}
		if req.ThinkingText {
			out.Thinking.Display = "summarized"
		}
	}
	return out, nil
}

func toOpenAITools(tools []sdk.ToolUnionParam) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		if tool.OfTool == nil {
			continue
		}
		desc := ""
		if tool.OfTool.Description.Valid() {
			desc = tool.OfTool.Description.Value
		}
		params := map[string]any{
			"type":       "object",
			"properties": tool.OfTool.InputSchema.Properties,
		}
		if len(tool.OfTool.InputSchema.Required) > 0 {
			params["required"] = tool.OfTool.InputSchema.Required
		}
		// Preserve ExtraFields (additionalProperties, etc.) when present.
		for key, value := range tool.OfTool.InputSchema.ExtraFields {
			params[key] = value
		}
		out = append(out, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.OfTool.Name,
				Description: desc,
				Parameters:  params,
			},
		})
	}
	return out
}

func toOpenAIMessages(system string, messages []sdk.MessageParam) ([]openAIChatMessage, error) {
	out := make([]openAIChatMessage, 0, len(messages)+1)
	if text := strings.TrimSpace(system); text != "" {
		out = append(out, openAIChatMessage{Role: "system", Content: text})
	}
	for _, message := range messages {
		converted, err := toOpenAIMessage(message)
		if err != nil {
			return nil, err
		}
		out = append(out, converted...)
	}
	return out, nil
}

func toOpenAIMessage(message sdk.MessageParam) ([]openAIChatMessage, error) {
	role := string(message.Role)
	switch role {
	case "user":
		return toOpenAIUserMessages(message)
	case "assistant":
		return toOpenAIAssistantMessages(message)
	case "system":
		text := messageTextContent(message)
		if text == "" {
			return nil, nil
		}
		return []openAIChatMessage{{Role: "system", Content: text}}, nil
	default:
		return nil, fmt.Errorf("unsupported message role %q for openai format", role)
	}
}

func toOpenAIUserMessages(message sdk.MessageParam) ([]openAIChatMessage, error) {
	// OpenAI expects tool results as separate role=tool messages.
	// Multimodal images inside tool_result are not reliably accepted on role=tool,
	// so we emit the tool text first and follow with a user message that carries
	// the actual image_url parts (ViewImage / multimodal tool results).
	var out []openAIChatMessage
	var parts []openAIContentPart
	var plain strings.Builder

	for _, block := range message.Content {
		switch {
		case block.OfToolResult != nil:
			tr := block.OfToolResult
			text, images, err := toolResultOpenAIContent(tr)
			if err != nil {
				return nil, err
			}
			if text == "" && len(images) > 0 {
				text = "Image content follows for this tool result."
			}
			out = append(out, openAIChatMessage{
				Role:       "tool",
				ToolCallID: tr.ToolUseID,
				Content:    text,
			})
			if len(images) > 0 {
				// Dedicated user turn so vision models actually receive the pixels.
				imgParts := make([]openAIContentPart, 0, len(images)+1)
				imgParts = append(imgParts, openAIContentPart{
					Type: "text",
					Text: fmt.Sprintf("[image from tool result tool_call_id=%s]", tr.ToolUseID),
				})
				imgParts = append(imgParts, images...)
				out = append(out, openAIChatMessage{Role: "user", Content: imgParts})
			}
		case block.OfText != nil:
			if plain.Len() > 0 {
				plain.WriteString("\n")
			}
			plain.WriteString(block.OfText.Text)
			parts = append(parts, openAIContentPart{Type: "text", Text: block.OfText.Text})
		case block.OfImage != nil:
			url, err := imageDataURL(block.OfImage)
			if err != nil {
				return nil, err
			}
			parts = append(parts, openAIContentPart{
				Type:     "image_url",
				ImageURL: &openAIImageURL{URL: url},
			})
		}
	}

	if len(parts) == 0 {
		return out, nil
	}
	// Prefer simple string content when there is only text.
	if len(parts) == 1 && parts[0].Type == "text" {
		out = append(out, openAIChatMessage{Role: "user", Content: parts[0].Text})
		return out, nil
	}
	hasImage := false
	for _, p := range parts {
		if p.Type == "image_url" {
			hasImage = true
			break
		}
	}
	if !hasImage {
		out = append(out, openAIChatMessage{Role: "user", Content: plain.String()})
		return out, nil
	}
	// Keep text+image as a single multimodal user message for @image attachments.
	out = append(out, openAIChatMessage{Role: "user", Content: parts})
	return out, nil
}

func toOpenAIAssistantMessages(message sdk.MessageParam) ([]openAIChatMessage, error) {
	var text strings.Builder
	var toolCalls []openAIToolCall
	for _, block := range message.Content {
		switch {
		case block.OfText != nil:
			text.WriteString(block.OfText.Text)
		case block.OfThinking != nil:
			// OpenAI-compatible gateways typically do not replay thinking blocks.
			// Drop them to keep the prefix stable for cache hits.
		case block.OfToolUse != nil:
			args, err := json.Marshal(block.OfToolUse.Input)
			if err != nil {
				return nil, fmt.Errorf("marshal tool_use input: %w", err)
			}
			toolCalls = append(toolCalls, openAIToolCall{
				ID:   block.OfToolUse.ID,
				Type: "function",
				Function: openAIFunctionCall{
					Name:      block.OfToolUse.Name,
					Arguments: string(args),
				},
			})
		}
	}
	msg := openAIChatMessage{Role: "assistant"}
	if text.Len() > 0 {
		msg.Content = text.String()
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	if msg.Content == nil && len(msg.ToolCalls) == 0 {
		msg.Content = ""
	}
	return []openAIChatMessage{msg}, nil
}

func messageTextContent(message sdk.MessageParam) string {
	var b strings.Builder
	for _, block := range message.Content {
		if block.OfText != nil {
			b.WriteString(block.OfText.Text)
		}
	}
	return b.String()
}

func toolResultText(tr *sdk.ToolResultBlockParam) string {
	text, _, _ := toolResultOpenAIContent(tr)
	return text
}

// toolResultOpenAIContent extracts caption text and any image parts from an
// Anthropic tool_result block. Images are returned as OpenAI image_url parts.
func toolResultOpenAIContent(tr *sdk.ToolResultBlockParam) (string, []openAIContentPart, error) {
	if tr == nil {
		return "", nil, nil
	}
	var b strings.Builder
	var images []openAIContentPart
	for _, part := range tr.Content {
		switch {
		case part.OfText != nil:
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part.OfText.Text)
		case part.OfImage != nil:
			url, err := imageDataURL(part.OfImage)
			if err != nil {
				return "", nil, err
			}
			images = append(images, openAIContentPart{
				Type:     "image_url",
				ImageURL: &openAIImageURL{URL: url},
			})
		}
	}
	return b.String(), images, nil
}

func imageDataURL(img *sdk.ImageBlockParam) (string, error) {
	if img == nil {
		return "", fmt.Errorf("nil image block")
	}
	if img.Source.OfBase64 != nil {
		mime := strings.TrimSpace(string(img.Source.OfBase64.MediaType))
		if mime == "" {
			mime = "image/png"
		}
		data := strings.TrimSpace(img.Source.OfBase64.Data)
		if data == "" {
			return "", fmt.Errorf("empty base64 image data")
		}
		// Some gateways are picky about whitespace/newlines in data URLs.
		data = strings.ReplaceAll(data, "\n", "")
		data = strings.ReplaceAll(data, "\r", "")
		return "data:" + mime + ";base64," + data, nil
	}
	if img.Source.OfURL != nil {
		url := strings.TrimSpace(img.Source.OfURL.URL)
		if url == "" {
			return "", fmt.Errorf("empty image url")
		}
		return url, nil
	}
	return "", fmt.Errorf("unsupported image source for openai format")
}

func decodeOpenAIResponse(r io.Reader) (*sdk.Message, error) {
	var resp openAIChatResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if resp.Error != nil && strings.TrimSpace(resp.Error.Message) != "" {
		return nil, fmt.Errorf("openai error: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai response has no choices")
	}
	return openAIChoiceToMessage(resp.Choices[0], resp.Model, resp.Usage), nil
}

func (c *Client) consumeOpenAIStream(r io.Reader, req MessageRequest) (*sdk.Message, error) {
	scanner := bufio.NewScanner(r)
	// Tool-heavy streams can emit large SSE chunks.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		model        string
		finishReason string
		text         strings.Builder
		thinking     strings.Builder
		toolCalls    = map[int]*openAIToolCall{}
		toolOrder    []int
		usage        *openAIUsage
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk openAIChatResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
			return nil, fmt.Errorf("openai stream error: %s", chunk.Error.Message)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		delta := choice.Delta
		if s := contentAsString(delta.Content); s != "" {
			text.WriteString(s)
			if req.OnTextDelta != nil {
				req.OnTextDelta(s)
			}
		}
		if s := reasoningContentAsString(delta); s != "" {
			thinking.WriteString(s)
			if req.OnThinkingDelta != nil {
				req.OnThinkingDelta(s)
			}
		}
		// Some gateways stream reasoning under alternate keys; ignore if absent.
		if len(delta.ToolCalls) > 0 {
			for _, tc := range delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				cur, ok := toolCalls[idx]
				if !ok {
					copied := tc
					if copied.Type == "" {
						copied.Type = "function"
					}
					toolCalls[idx] = &copied
					toolOrder = append(toolOrder, idx)
					cur = toolCalls[idx]
				} else {
					if tc.ID != "" {
						cur.ID = tc.ID
					}
					if tc.Function.Name != "" {
						cur.Function.Name = tc.Function.Name
					}
					cur.Function.Arguments += tc.Function.Arguments
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read openai stream: %w", err)
	}

	// Build a synthetic non-stream choice for the shared mapper.
	msg := openAIChatMessage{
		Role:             "assistant",
		Content:          text.String(),
		ReasoningContent: thinking.String(),
	}
	if len(toolOrder) > 0 {
		msg.ToolCalls = make([]openAIToolCall, 0, len(toolOrder))
		for _, idx := range toolOrder {
			if tc := toolCalls[idx]; tc != nil {
				msg.ToolCalls = append(msg.ToolCalls, *tc)
			}
		}
	}
	return openAIChoiceToMessage(openAIChatChoice{
		Message:      msg,
		FinishReason: finishReason,
	}, model, usage), nil
}

func reasoningContentAsString(message openAIChatMessage) string {
	for _, value := range []any{message.ReasoningContent, message.Reasoning, message.Thinking} {
		if text := contentAsString(value); text != "" {
			return text
		}
	}
	return ""
}

func contentAsString(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t == "text" {
				if s, _ := m["text"].(string); s != "" {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	default:
		// encoding/json may give []openAIContentPart-like maps already handled.
		raw, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		var parts []openAIContentPart
		if err := json.Unmarshal(raw, &parts); err == nil {
			var b strings.Builder
			for _, p := range parts {
				if p.Type == "text" {
					b.WriteString(p.Text)
				}
			}
			return b.String()
		}
		return ""
	}
}

func openAIChoiceToMessage(choice openAIChatChoice, model string, usage *openAIUsage) *sdk.Message {
	msg := choice.Message
	// Some providers only fill delta in stream final; prefer message.
	text := contentAsString(msg.Content)
	thinking := reasoningContentAsString(msg)
	content := make([]sdk.ContentBlockUnion, 0, 2+len(msg.ToolCalls))
	if thinking != "" {
		content = append(content, mustContentBlock(map[string]any{
			"type":      "thinking",
			"thinking":  thinking,
			"signature": "",
		}))
	}
	if text != "" {
		content = append(content, mustContentBlock(map[string]any{
			"type": "text",
			"text": text,
		}))
	}
	for i, tc := range msg.ToolCalls {
		id := strings.TrimSpace(tc.ID)
		if id == "" {
			id = fmt.Sprintf("toolu_openai_%d", i)
		}
		name := tc.Function.Name
		var input any
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			input = map[string]any{}
		} else if err := json.Unmarshal([]byte(args), &input); err != nil {
			// Keep raw string if not valid JSON so the engine can surface an error.
			input = map[string]any{"raw": args}
		}
		content = append(content, mustContentBlock(map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  name,
			"input": input,
		}))
	}

	stop := mapOpenAIFinishReason(choice.FinishReason, len(msg.ToolCalls) > 0)
	out := &sdk.Message{
		Type:       "message",
		Role:       "assistant",
		Model:      sdk.Model(model),
		Content:    content,
		StopReason: stop,
		Usage:      mapOpenAIUsage(usage),
	}
	return out
}

// mustContentBlock builds a ContentBlockUnion via JSON so SDK union helpers
// (AsToolUse/ToParam) have RawJSON metadata populated.
func mustContentBlock(v any) sdk.ContentBlockUnion {
	raw, err := json.Marshal(v)
	if err != nil {
		return sdk.ContentBlockUnion{Type: "text", Text: ""}
	}
	var block sdk.ContentBlockUnion
	if err := json.Unmarshal(raw, &block); err != nil {
		return sdk.ContentBlockUnion{Type: "text", Text: string(raw)}
	}
	return block
}

func mapOpenAIFinishReason(reason string, hasTools bool) sdk.StopReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool_calls", "function_call":
		return sdk.StopReasonToolUse
	case "length", "max_tokens":
		return sdk.StopReasonMaxTokens
	case "content_filter":
		return sdk.StopReasonRefusal
	case "stop", "end_turn", "":
		if hasTools {
			return sdk.StopReasonToolUse
		}
		return sdk.StopReasonEndTurn
	default:
		if hasTools {
			return sdk.StopReasonToolUse
		}
		return sdk.StopReasonEndTurn
	}
}

func mapOpenAIUsage(usage *openAIUsage) sdk.Usage {
	if usage == nil {
		return sdk.Usage{}
	}
	cacheRead := usage.CacheReadInputTokens
	if cacheRead == 0 && usage.PromptTokensDetails != nil {
		cacheRead = usage.PromptTokensDetails.CachedTokens
	}
	// Anthropic-style: input_tokens is the uncached portion after the breakpoint.
	// OpenAI-style: prompt_tokens is the full prompt. Best-effort split:
	input := usage.PromptTokens
	if cacheRead > 0 && input >= cacheRead {
		input = input - cacheRead
	}
	return sdk.Usage{
		InputTokens:              input,
		OutputTokens:             usage.CompletionTokens,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
}
