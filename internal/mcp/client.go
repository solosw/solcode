package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/tool"
)

type Client interface {
	Start(ctx context.Context) error
	ListTools(ctx context.Context) ([]tool.MCPToolInfo, error)
	CallTool(ctx context.Context, toolName string, input json.RawMessage) (*tool.ContentBlock, error)
	Close() error
}

type ClientFactory func(server config.MCPServerConfig) Client

func NewClientFactory() ClientFactory {
	return func(server config.MCPServerConfig) Client {
		switch normalizeTransport(server.Transport) {
		case "", "stdio":
			return NewStdioClient(server)
		case "sse":
			return NewSSEClient(server)
		case "http", "streamable":
			return NewStreamableClient(server)
		default:
			return &unsupportedClient{server: server}
		}
	}
}

type unsupportedClient struct {
	server config.MCPServerConfig
}

func (c *unsupportedClient) Start(ctx context.Context) error {
	_ = ctx
	return validateServerConfig(c.server)
}

func (c *unsupportedClient) ListTools(ctx context.Context) ([]tool.MCPToolInfo, error) {
	_ = ctx
	return nil, validateServerConfig(c.server)
}

func (c *unsupportedClient) CallTool(ctx context.Context, toolName string, input json.RawMessage) (*tool.ContentBlock, error) {
	_ = ctx
	_ = toolName
	_ = input
	return nil, validateServerConfig(c.server)
}

func (c *unsupportedClient) Close() error { return nil }

func toolInfoFromSDK(serverName string, sdkTool *sdkmcp.Tool) tool.MCPToolInfo {
	info := tool.MCPToolInfo{
		ServerName:  serverName,
		ToolName:    sdkTool.Name,
		Description: sdkTool.Description,
	}
	if info.Description == "" {
		info.Description = sdkTool.Title
	}
	if schema, ok := sdkTool.InputSchema.(map[string]any); ok {
		info.InputSchema = schema
	}
	return info
}

func contentBlockFromCallResult(result *sdkmcp.CallToolResult) (*tool.ContentBlock, error) {
	if result == nil {
		return tool.ErrorResult("mcp tool returned nil result"), nil
	}
	text := strings.TrimSpace(contentToText(result.Content))
	if text == "" && result.StructuredContent != nil {
		b, err := json.Marshal(result.StructuredContent)
		if err == nil {
			text = string(b)
		}
	}
	if text == "" {
		text = "tool executed successfully (no output)"
	}
	if result.IsError {
		return tool.ErrorResult(text), nil
	}
	return tool.Result(text), nil
}

func contentToText(items []sdkmcp.Content) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case *sdkmcp.TextContent:
			if strings.TrimSpace(value.Text) != "" {
				parts = append(parts, value.Text)
			}
		default:
			if b, err := json.Marshal(item); err == nil {
				parts = append(parts, string(b))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func validateServerConfig(server config.MCPServerConfig) error {
	if strings.TrimSpace(server.Name) == "" {
		return fmt.Errorf("mcp server name is required")
	}
	switch normalizeTransport(server.Transport) {
	case "", "stdio":
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("mcp server %q requires command for stdio transport", server.Name)
		}
		if strings.TrimSpace(server.URL) != "" {
			return fmt.Errorf("mcp server %q stdio transport must not set url", server.Name)
		}
		return nil
	case "sse":
		if strings.TrimSpace(server.URL) == "" {
			return fmt.Errorf("mcp server %q requires url for sse transport", server.Name)
		}
		if strings.TrimSpace(server.Command) != "" {
			return fmt.Errorf("mcp server %q sse transport must not set command", server.Name)
		}
		return nil
	case "http", "streamable":
		if strings.TrimSpace(server.URL) == "" {
			return fmt.Errorf("mcp server %q requires url for http transport", server.Name)
		}
		if strings.TrimSpace(server.Command) != "" {
			return fmt.Errorf("mcp server %q http transport must not set command", server.Name)
		}
		return nil
	default:
		return fmt.Errorf("mcp server %q transport %q is not implemented yet", server.Name, server.Transport)
	}
}

func normalizeTransport(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}
