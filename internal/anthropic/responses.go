package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// Responses is stateless here: the engine owns history, so every request sends
// the full input and disables server-side storage. Opaque encrypted reasoning
// is carried through SDK thinking signatures to survive tool turns and sessions.
const responsesReasoningSignaturePrefix = "solcode:responses:"

type responsesRequest struct {
	Model           string               `json:"model"`
	Instructions    string               `json:"instructions,omitempty"`
	Input           []responsesInputItem `json:"input"`
	Tools           []responsesTool      `json:"tools,omitempty"`
	MaxOutputTokens int64                `json:"max_output_tokens"`
	Stream          bool                 `json:"stream"`
	Store           bool                 `json:"store"`
	Include         []string             `json:"include,omitempty"`
	Reasoning       *responsesReasoning  `json:"reasoning,omitempty"`
}

type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responsesTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
	// Existing tools allow optional properties; do not opt into strict schemas.
	Strict bool `json:"strict"`
}

type responsesInputItem struct {
	Type      string                 `json:"type"`
	Role      string                 `json:"role,omitempty"`
	Content   []responsesContentPart `json:"content,omitempty"`
	CallID    string                 `json:"call_id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments string                 `json:"arguments,omitempty"`
	Output    *string                `json:"output,omitempty"`
	// Only reasoning items carry these fields. A pointer keeps an empty summary
	// array on the wire without adding it to message/function-call items.
	ID               string                  `json:"id,omitempty"`
	Summary          *[]responsesContentPart `json:"summary,omitempty"`
	EncryptedContent string                  `json:"encrypted_content,omitempty"`
}

type responsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Refusal  string `json:"refusal,omitempty"`
}

// Text is mandatory on text input parts, including an empty string; images
// must not carry the unrelated text field.
func (p responsesContentPart) MarshalJSON() ([]byte, error) {
	if p.Type == "input_text" || p.Type == "output_text" {
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: p.Type, Text: p.Text})
	}
	type plain responsesContentPart
	return json.Marshal(plain(p))
}

type responsesOutputItem struct {
	ID               string                 `json:"id"`
	Type             string                 `json:"type"`
	Role             string                 `json:"role"`
	Content          []responsesContentPart `json:"content"`
	Summary          []responsesContentPart `json:"summary"`
	CallID           string                 `json:"call_id"`
	Name             string                 `json:"name"`
	Arguments        string                 `json:"arguments"`
	EncryptedContent string                 `json:"encrypted_content"`
}

type responsesResponse struct {
	ID                string                `json:"id"`
	Model             string                `json:"model"`
	Status            string                `json:"status"`
	Output            []responsesOutputItem `json:"output"`
	Usage             *responsesUsage       `json:"usage"`
	Error             *openAIError          `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type responsesUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func responsesEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case strings.HasSuffix(baseURL, "/responses"):
		return baseURL
	case strings.HasSuffix(baseURL, "/v1"):
		return baseURL + "/responses"
	default:
		return baseURL + "/v1/responses"
	}
}

func (c *Client) createResponses(ctx context.Context, req MessageRequest) (*sdk.Message, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("responses format requires base_url")
	}
	body, err := buildResponsesRequest(req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal responses request: %w", err)
	}
	client := c.http
	if client == nil {
		client = http.DefaultClient
	}
	if !req.Stream {
		client = c.idleHTTPClient()
	}
	resp, err := doOpenAIRequest(ctx, client, responsesEndpoint(c.baseURL), raw, c.apiKey, req.Stream)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("openai responses %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if req.Stream {
		return consumeResponsesStream(resp.Body, req)
	}
	var result responsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode responses response: %w", err)
	}
	return responsesToMessage(result)
}

func buildResponsesRequest(req MessageRequest) (responsesRequest, error) {
	model := req.Model
	if model == "" {
		model = DefaultModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16_000
	}
	out := responsesRequest{
		Model: model, Instructions: req.System, MaxOutputTokens: maxTokens,
		Input: make([]responsesInputItem, 0, len(req.Messages)), Stream: req.Stream,
		Include: []string{"reasoning.encrypted_content"},
	}
	for _, tool := range toOpenAITools(req.Tools) {
		out.Tools = append(out.Tools, responsesTool{
			Type: "function", Name: tool.Function.Name,
			Description: tool.Function.Description, Parameters: tool.Function.Parameters,
		})
	}
	if req.Effort != "" || (req.Thinking && req.ThinkingText) {
		out.Reasoning = &responsesReasoning{Effort: strings.TrimSpace(req.Effort)}
		if req.Thinking && req.ThinkingText {
			out.Reasoning.Summary = "auto"
		}
	}
	for _, message := range req.Messages {
		role := string(message.Role)
		if role != "user" && role != "assistant" && role != "system" {
			return responsesRequest{}, fmt.Errorf("unsupported message role %q for responses format", role)
		}
		var parts []responsesContentPart
		flush := func() {
			if len(parts) > 0 {
				out.Input = append(out.Input, responsesInputItem{Type: "message", Role: role, Content: parts})
				parts = nil
			}
		}
		for _, block := range message.Content {
			switch {
			case block.OfText != nil:
				partType := "input_text"
				if role == "assistant" {
					partType = "output_text"
				}
				parts = append(parts, responsesContentPart{Type: partType, Text: block.OfText.Text})
			case block.OfThinking != nil:
				// Native Anthropic signatures and plain summaries are not valid
				// Responses input; only replay our opaque Responses envelope.
				if signature, ok := strings.CutPrefix(block.OfThinking.Signature, responsesReasoningSignaturePrefix); ok {
					var reasoning responsesInputItem
					if err := json.Unmarshal([]byte(signature), &reasoning); err != nil {
						return responsesRequest{}, fmt.Errorf("decode responses reasoning history: %w", err)
					}
					if reasoning.Type != "reasoning" || reasoning.EncryptedContent == "" || reasoning.Summary == nil {
						return responsesRequest{}, fmt.Errorf("invalid responses reasoning history")
					}
					flush()
					out.Input = append(out.Input, reasoning)
				}
			case block.OfImage != nil:
				url, err := imageDataURL(block.OfImage)
				if err != nil {
					return responsesRequest{}, err
				}
				parts = append(parts, responsesContentPart{Type: "input_image", ImageURL: url})
			case block.OfToolUse != nil:
				flush()
				tool := block.OfToolUse
				args, err := json.Marshal(tool.Input)
				if err != nil {
					return responsesRequest{}, fmt.Errorf("marshal responses tool input: %w", err)
				}
				out.Input = append(out.Input, responsesInputItem{
					Type: "function_call", CallID: tool.ID, Name: tool.Name, Arguments: string(args),
				})
			case block.OfToolResult != nil:
				flush()
				tool := block.OfToolResult
				text, images, err := toolResultOpenAIContent(tool)
				if err != nil {
					return responsesRequest{}, err
				}
				out.Input = append(out.Input, responsesInputItem{
					Type: "function_call_output", CallID: tool.ToolUseID, Output: &text,
				})
				// Match the existing vision transport: keep tool output as text and
				// send the pixels in a following user message.
				if len(images) > 0 {
					imageParts := []responsesContentPart{{Type: "input_text", Text: fmt.Sprintf("[image from tool result tool_call_id=%s]", tool.ToolUseID)}}
					for _, image := range images {
						imageParts = append(imageParts, responsesContentPart{Type: "input_image", ImageURL: image.ImageURL.URL})
					}
					out.Input = append(out.Input, responsesInputItem{Type: "message", Role: "user", Content: imageParts})
				}
			}
		}
		flush()
	}
	return out, nil
}

func responsesToMessage(response responsesResponse) (*sdk.Message, error) {
	if response.Error != nil {
		return nil, fmt.Errorf("openai responses error: %s", response.Error.Message)
	}
	if response.Status != "completed" && response.Status != "incomplete" {
		return nil, fmt.Errorf("openai responses unexpected status %q", response.Status)
	}
	message := &sdk.Message{
		ID: response.ID, Type: "message", Role: "assistant", Model: sdk.Model(response.Model),
		StopReason: sdk.StopReasonEndTurn,
	}
	for _, item := range response.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					message.Content = append(message.Content, mustContentBlock(map[string]any{"type": "text", "text": part.Text}))
				case "refusal":
					message.Content = append(message.Content, mustContentBlock(map[string]any{"type": "text", "text": part.Refusal}))
					message.StopReason = sdk.StopReasonRefusal
				}
			}
		case "reasoning":
			var text strings.Builder
			for _, part := range item.Summary {
				text.WriteString(part.Text)
			}
			for _, part := range item.Content {
				text.WriteString(part.Text)
			}
			signature := ""
			if item.EncryptedContent != "" {
				summary := item.Summary
				if summary == nil {
					summary = []responsesContentPart{}
				}
				encoded, err := json.Marshal(responsesInputItem{
					Type: "reasoning", ID: item.ID, Summary: &summary, EncryptedContent: item.EncryptedContent,
				})
				if err != nil {
					return nil, fmt.Errorf("encode responses reasoning history: %w", err)
				}
				signature = responsesReasoningSignaturePrefix + string(encoded)
			}
			if text.Len() > 0 || signature != "" {
				message.Content = append(message.Content, mustContentBlock(map[string]any{"type": "thinking", "thinking": text.String(), "signature": signature}))
			}
		case "function_call":
			if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
				return nil, fmt.Errorf("openai responses function call missing call_id or name")
			}
			mapped := openAIChoiceToMessage(openAIChatChoice{Message: openAIChatMessage{ToolCalls: []openAIToolCall{{
				ID: item.CallID, Type: "function", Function: openAIFunctionCall{Name: item.Name, Arguments: item.Arguments},
			}}}}, response.Model, nil)
			message.Content = append(message.Content, mapped.Content...)
			message.StopReason = sdk.StopReasonToolUse
		}
	}
	if response.Status == "incomplete" {
		if response.IncompleteDetails == nil {
			return nil, fmt.Errorf("openai responses incomplete without a reason")
		}
		switch response.IncompleteDetails.Reason {
		case "max_output_tokens":
			message.StopReason = sdk.StopReasonMaxTokens
		case "content_filter":
			message.StopReason = sdk.StopReasonRefusal
		default:
			return nil, fmt.Errorf("openai responses incomplete: %s", response.IncompleteDetails.Reason)
		}
	}
	if response.Usage != nil {
		usage := &openAIUsage{PromptTokens: response.Usage.InputTokens, CompletionTokens: response.Usage.OutputTokens}
		if response.Usage.InputTokensDetails != nil {
			usage.CacheReadInputTokens = response.Usage.InputTokensDetails.CachedTokens
		}
		message.Usage = mapOpenAIUsage(usage)
	}
	return message, nil
}

// Only completed/incomplete is a successful terminal event. EOF without one is
// an interrupted response, never a successful partial tool call or answer.
func consumeResponsesStream(body io.Reader, req MessageRequest) (*sdk.Message, error) {
	items := make(map[int]*responsesOutputItem)
	var result responsesResponse
	var terminal bool
	handle := func(payload string, eventName string) error {
		if payload == "[DONE]" {
			return nil
		}
		var event struct {
			Type         string                `json:"type"`
			Response     *responsesResponse    `json:"response"`
			OutputIndex  int                   `json:"output_index"`
			ContentIndex int                   `json:"content_index"`
			SummaryIndex int                   `json:"summary_index"`
			Item         *responsesOutputItem  `json:"item"`
			Part         *responsesContentPart `json:"part"`
			Delta        string                `json:"delta"`
			Arguments    string                `json:"arguments"`
			Error        *openAIError          `json:"error"`
			Message      string                `json:"message"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("decode responses stream event: %w", err)
		}
		if event.Type == "" {
			event.Type = eventName
		}
		switch event.Type {
		case "error":
			if event.Error != nil {
				return fmt.Errorf("openai responses stream error: %s", event.Error.Message)
			}
			return fmt.Errorf("openai responses stream error: %s", event.Message)
		case "response.failed":
			if event.Response != nil && event.Response.Error != nil {
				return fmt.Errorf("openai responses failed: %s", event.Response.Error.Message)
			}
			return fmt.Errorf("openai responses failed")
		case "response.created", "response.in_progress":
			if event.Response != nil {
				result = *event.Response
			}
		case "response.completed", "response.incomplete":
			if event.Response == nil {
				return fmt.Errorf("openai responses terminal event missing response")
			}
			result = *event.Response
			terminal = true
		case "response.output_item.added", "response.output_item.done":
			if event.Item != nil {
				items[event.OutputIndex] = event.Item
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			item := items[event.OutputIndex]
			if item == nil {
				return fmt.Errorf("responses tool delta for unknown output item %d", event.OutputIndex)
			}
			if event.Type == "response.function_call_arguments.done" {
				item.Arguments = event.Arguments
			} else {
				item.Arguments += event.Delta
			}
		case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
			"response.output_text.delta", "response.refusal.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			item := items[event.OutputIndex]
			if item == nil {
				return fmt.Errorf("responses content event for unknown output item %d", event.OutputIndex)
			}
			parts, index := &item.Content, event.ContentIndex
			if strings.HasPrefix(event.Type, "response.reasoning_summary_") {
				parts, index = &item.Summary, event.SummaryIndex
			}
			if index < 0 || index > 100_000 {
				return fmt.Errorf("responses invalid content index %d", index)
			}
			for len(*parts) <= index {
				*parts = append(*parts, responsesContentPart{})
			}
			part := &(*parts)[index]
			if event.Part != nil {
				*part = *event.Part
			}
			switch event.Type {
			case "response.output_text.delta":
				part.Type = "output_text"
				part.Text += event.Delta
				if req.OnTextDelta != nil {
					req.OnTextDelta(event.Delta)
				}
			case "response.refusal.delta":
				part.Type = "refusal"
				part.Refusal += event.Delta
				if req.OnTextDelta != nil {
					req.OnTextDelta(event.Delta)
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				part.Type = "summary_text"
				part.Text += event.Delta
				if req.OnThinkingDelta != nil {
					req.OnThinkingDelta(event.Delta)
				}
			}
		}
		return nil
	}
	var data strings.Builder
	var eventName string
	flush := func() error {
		payload, name := data.String(), eventName
		data.Reset()
		eventName = ""
		if payload == "" {
			return nil
		}
		return handle(payload, name)
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if terminal {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read responses stream: %w", err)
	}
	if !terminal {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	if !terminal {
		return nil, fmt.Errorf("responses stream ended before a terminal event")
	}
	if result.Output == nil {
		// Some compatible gateways omit the terminal output. Keep the streamed
		// items in output-index order instead of losing text or parallel calls.
		indices := make([]int, 0, len(items))
		for index := range items {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		for _, index := range indices {
			result.Output = append(result.Output, *items[index])
		}
	}
	return responsesToMessage(result)
}
