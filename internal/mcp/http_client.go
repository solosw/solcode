package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/tool"
)

type sseClient struct {
	server config.MCPServerConfig

	mu      sync.Mutex
	client  *sdkmcp.Client
	session *sdkmcp.ClientSession
}

func NewSSEClient(server config.MCPServerConfig) Client {
	return &sseClient{server: server}
}

func (c *sseClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateServerConfig(c.server); err != nil {
		return err
	}
	if c.session != nil {
		return nil
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "codeplus-agent", Version: "0.1.0"}, nil)
	httpClient := newHeaderHTTPClient(c.server.Headers)
	session, err := client.Connect(ctx, &sdkmcp.SSEClientTransport{Endpoint: c.server.URL, HTTPClient: httpClient}, nil)
	if err != nil {
		return err
	}
	c.client = client
	c.session = session
	return nil
}

func (c *sseClient) ListTools(ctx context.Context) ([]tool.MCPToolInfo, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	result, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	out := make([]tool.MCPToolInfo, 0, len(result.Tools))
	for _, item := range result.Tools {
		out = append(out, toolInfoFromSDK(c.server.Name, item))
	}
	return out, nil
}

func (c *sseClient) CallTool(ctx context.Context, toolName string, input json.RawMessage) (*tool.ContentBlock, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.ErrorResult("invalid tool input: " + err.Error()), nil
	}
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	return contentBlockFromCallResult(result)
}

func (c *sseClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	if c.session != nil {
		err = c.session.Close()
		c.session = nil
	}
	c.client = nil
	return err
}

type streamableClient struct {
	server config.MCPServerConfig

	mu      sync.Mutex
	client  *sdkmcp.Client
	session *sdkmcp.ClientSession
}

func NewStreamableClient(server config.MCPServerConfig) Client {
	return &streamableClient{server: server}
}

func (c *streamableClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateServerConfig(c.server); err != nil {
		return err
	}
	if c.session != nil {
		return nil
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "codeplus-agent", Version: "0.1.0"}, nil)
	httpClient := newHeaderHTTPClient(c.server.Headers)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: c.server.URL, HTTPClient: httpClient, MaxRetries: 5}, nil)
	if err != nil {
		return err
	}
	c.client = client
	c.session = session
	return nil
}

func (c *streamableClient) ListTools(ctx context.Context) ([]tool.MCPToolInfo, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	result, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	out := make([]tool.MCPToolInfo, 0, len(result.Tools))
	for _, item := range result.Tools {
		out = append(out, toolInfoFromSDK(c.server.Name, item))
	}
	return out, nil
}

func (c *streamableClient) CallTool(ctx context.Context, toolName string, input json.RawMessage) (*tool.ContentBlock, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.ErrorResult("invalid tool input: " + err.Error()), nil
	}
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	return contentBlockFromCallResult(result)
}

func (c *streamableClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	if c.session != nil {
		err = c.session.Close()
		c.session = nil
	}
	c.client = nil
	return err
}

const defaultMCPHTTPTimeout = 2 * time.Minute

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (r *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = clone.Header.Clone()
	for key, value := range r.headers {
		clone.Header.Set(key, value)
	}
	return r.base.RoundTrip(clone)
}

func newHeaderHTTPClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{Timeout: defaultMCPHTTPTimeout, Transport: &headerRoundTripper{base: http.DefaultTransport, headers: headers}}
}

func NewTestHeaderRoundTripper(base http.RoundTripper, headers map[string]string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &headerRoundTripper{base: base, headers: headers}
}
