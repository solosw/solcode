package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/solosw/solcode/internal/httpproxy"
)

const DefaultModel = "claude-opus-4-8"
const DefaultMaxRetries = 5

// Wire formats for outbound chat requests.
const (
	// FormatAnthropic sends native Anthropic Messages API requests (default).
	FormatAnthropic = "anthropic"
	// FormatOpenAI sends OpenAI chat/completions-compatible requests to
	// {base_url}/chat/completions. Internal engine types stay Anthropic SDK shaped.
	FormatOpenAI = "openai"
	// FormatResponses sends OpenAI Responses API requests to {base_url}/v1/responses.
	FormatResponses = "responses"
)

// Options configures the API client wrapper.
type Options struct {
	APIKey  string
	BaseURL string
	// Format selects the outbound protocol. Empty defaults to anthropic.
	Format string
}

// Client is a thin wrapper around the official Anthropic Go SDK, with an optional
// OpenAI Chat Completions and Responses transports for compatible gateways.
type Client struct {
	sdk     sdk.Client
	format  string
	apiKey  string
	baseURL string
	http    *http.Client
}

func NewClient(opts Options) *Client {
	format := NormalizeFormat(opts.Format)
	requestOptions := make([]option.RequestOption, 0, 3)
	requestOptions = append(requestOptions, option.WithMaxRetries(DefaultMaxRetries))
	if opts.APIKey != "" {
		requestOptions = append(requestOptions, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		requestOptions = append(requestOptions, option.WithBaseURL(opts.BaseURL))
	}
	return &Client{
		sdk:     sdk.NewClient(requestOptions...),
		format:  format,
		apiKey:  opts.APIKey,
		baseURL: strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		http:    httpproxy.NewClient(0), // stream can be long-lived; proxy-aware
	}
}

// NormalizeFormat returns a supported wire format; unknown values fall back to anthropic.
func NormalizeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatOpenAI, "chat", "chat_completions", "chat-completions":
		return FormatOpenAI
	case FormatResponses, "openai_responses", "openai-responses":
		return FormatResponses
	default:
		return FormatAnthropic
	}
}

func (c *Client) Format() string {
	if c == nil {
		return FormatAnthropic
	}
	return NormalizeFormat(c.format)
}

func (c *Client) SDK() *sdk.Client {
	if c == nil {
		return nil
	}
	return &c.sdk
}

// Create sends one request using the selected wire protocol and maps the response
// into an Anthropic sdk.Message. When Stream is true, deltas are delivered via
// OnTextDelta / OnThinkingDelta callbacks.
func (c *Client) Create(ctx context.Context, req MessageRequest) (*sdk.Message, error) {
	if c == nil {
		return nil, fmt.Errorf("anthropic client is nil")
	}
	switch c.Format() {
	case FormatOpenAI:
		return c.createOpenAI(ctx, req)
	case FormatResponses:
		return c.createResponses(ctx, req)
	}
	return c.createAnthropic(ctx, req)
}

func (c *Client) createAnthropic(ctx context.Context, req MessageRequest) (*sdk.Message, error) {
	body, err := req.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}
	endpoint := strings.TrimRight(c.baseURL, "/")
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}
	if !strings.HasSuffix(endpoint, "/v1/messages") {
		endpoint += "/v1/messages"
	}
	resp, err := doAnthropicRequest(ctx, c.http, endpoint, body, c.apiKey)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("anthropic API %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if !req.Stream {
		return decodeAnthropicMessage(resp.Body)
	}
	return decodeAnthropicStream(resp.Body, req)
}

var (
	anthropicRetryDelay    = time.Second
	anthropicRetryMaxDelay = 30 * time.Second
)

func doAnthropicRequest(ctx context.Context, client *http.Client, endpoint string, body []byte, apiKey string) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	var lastErr error
	for attempt := 0; attempt <= DefaultMaxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create anthropic request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			httpReq.Header.Set("x-api-key", apiKey)
		}

		resp, err := client.Do(httpReq)
		if err == nil && !retryableAnthropicStatus(resp.StatusCode) {
			return resp, nil
		}
		if err == nil {
			lastErr = fmt.Errorf("anthropic API %s", resp.Status)
			if attempt == DefaultMaxRetries {
				return resp, nil
			}
			resp.Body.Close()
		} else {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !retryableAnthropicError(err) || attempt == DefaultMaxRetries {
				return nil, fmt.Errorf("send anthropic request: %w", err)
			}
			lastErr = err
		}

		delay := retryAfter(resp)
		if delay <= 0 {
			delay = exponentialRetryDelay(anthropicRetryDelay, attempt, anthropicRetryMaxDelay)
		}
		if err := waitForRetry(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("send anthropic request after retries: %w", lastErr)
}

func retryableAnthropicStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusConflict || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func retryableAnthropicError(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return max(time.Until(when), 0)
	}
	return 0
}

func exponentialRetryDelay(base time.Duration, attempt int, ceiling time.Duration) time.Duration {
	for range attempt {
		if base >= ceiling/2 {
			return ceiling
		}
		base *= 2
	}
	return min(base, ceiling)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func decodeAnthropicMessage(body io.Reader) (*sdk.Message, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read anthropic response: %w", err)
	}
	var message sdk.Message
	if err := json.Unmarshal(data, &message); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}
	return &message, nil
}

func decodeAnthropicStream(body io.Reader, req MessageRequest) (*sdk.Message, error) {
	var message map[string]any
	blocks := make(map[int]map[string]any)
	var blockOrder []int

	handle := func(payload []byte) error {
		var event struct {
			Type         string         `json:"type"`
			Index        int            `json:"index"`
			Message      map[string]any `json:"message"`
			ContentBlock map[string]any `json:"content_block"`
			Delta        map[string]any `json:"delta"`
			Usage        map[string]any `json:"usage"`
			Error        any            `json:"error"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode anthropic stream event: %w", err)
		}
		switch event.Type {
		case "error":
			return fmt.Errorf("anthropic stream error: %v", event.Error)
		case "message_start":
			message = event.Message
		case "content_block_start":
			blocks[event.Index] = event.ContentBlock
			blockOrder = append(blockOrder, event.Index)
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				return fmt.Errorf("anthropic stream delta for unknown content block %d", event.Index)
			}
			deltaType, _ := event.Delta["type"].(string)
			switch deltaType {
			case "text_delta":
				text, _ := event.Delta["text"].(string)
				previous, _ := block["text"].(string)
				block["text"] = previous + text
				if req.OnTextDelta != nil {
					req.OnTextDelta(text)
				}
			case "thinking_delta":
				thinking, _ := event.Delta["thinking"].(string)
				previous, _ := block["thinking"].(string)
				block["thinking"] = previous + thinking
				if req.OnThinkingDelta != nil {
					req.OnThinkingDelta(thinking)
				}
			case "input_json_delta":
				partial, _ := event.Delta["partial_json"].(string)
				previous, _ := block["input"].(string)
				block["input"] = previous + partial
			case "signature_delta":
				block["signature"] = event.Delta["signature"]
			}
		case "message_delta":
			if message == nil {
				return fmt.Errorf("anthropic stream message delta before message start")
			}
			for key, value := range event.Delta {
				message[key] = value
			}
			if len(event.Usage) > 0 {
				message["usage"] = event.Usage
			}
		}
		return nil
	}

	var eventName string
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := append([]byte(nil), data.String()...)
		data.Reset()
		if eventName == "ping" {
			return nil
		}
		return handle(payload)
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
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read anthropic stream: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if message == nil {
		return nil, fmt.Errorf("anthropic stream ended before message_start")
	}
	content := make([]map[string]any, 0, len(blockOrder))
	for _, index := range blockOrder {
		block := blocks[index]
		if input, ok := block["input"].(string); ok {
			var value any
			if err := json.Unmarshal([]byte(input), &value); err != nil {
				return nil, fmt.Errorf("decode tool input: %w", err)
			}
			block["input"] = value
		}
		content = append(content, block)
	}
	message["content"] = content
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode accumulated anthropic message: %w", err)
	}
	var result sdk.Message
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, fmt.Errorf("decode accumulated anthropic message: %w", err)
	}
	return &result, nil
}

// idleHTTPTimeout is only used for non-stream OpenAI requests.
func (c *Client) idleHTTPClient() *http.Client {
	if c == nil || c.http == nil {
		return httpproxy.NewClient(10 * time.Minute)
	}
	return &http.Client{Timeout: 10 * time.Minute, Transport: c.http.Transport}
}
