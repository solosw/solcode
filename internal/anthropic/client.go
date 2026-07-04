package anthropic

import (
	"context"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const DefaultModel = "claude-opus-4-8"

// Options configures the Anthropic API client wrapper.
type Options struct {
	APIKey  string
	BaseURL string
}

// Client is a thin wrapper around the official Anthropic Go SDK. It keeps the
// rest of codeplus-agent from depending on SDK construction details while still
// preserving SDK message/content types for safe multi-turn tool-use replay.
type Client struct {
	sdk sdk.Client
}

func NewClient(opts Options) *Client {
	requestOptions := make([]option.RequestOption, 0, 2)
	if opts.APIKey != "" {
		requestOptions = append(requestOptions, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		requestOptions = append(requestOptions, option.WithBaseURL(opts.BaseURL))
	}
	return &Client{sdk: sdk.NewClient(requestOptions...)}
}

func (c *Client) SDK() *sdk.Client {
	if c == nil {
		return nil
	}
	return &c.sdk
}

// Create sends one Messages API request. When Stream is true, it uses the SDK's
// streaming API and accumulates the final Message so callers can continue the
// conversation without losing tool_use/thinking blocks.
func (c *Client) Create(ctx context.Context, req MessageRequest) (*sdk.Message, error) {
	if c == nil {
		return nil, fmt.Errorf("anthropic client is nil")
	}
	params := req.ToSDKParams()
	if !req.Stream {
		return c.sdk.Messages.New(ctx, params)
	}

	stream := c.sdk.Messages.NewStreaming(ctx, params)
	message := sdk.Message{}
	for stream.Next() {
		event := stream.Current()
		dispatchStreamCallbacks(req, event)
		if err := message.Accumulate(event); err != nil {
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return &message, nil
}
