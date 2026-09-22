package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
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
		case config.MCPTransportStreamableHTTP:
			// Legacy "sse"/"http" configs are normalized to STREAMABLE_HTTP.
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
		// StructuredContent is a Go value, not a string, so render it directly
		// rather than marshaling to JSON first.
		text = structuredToText(result.StructuredContent)
	}
	if text == "" {
		text = "tool executed successfully (no output)"
	} else {
		// Servers frequently return a JSON document as text. Those are rendered
		// as readable key/value lines so the model reasons about the result
		// instead of parsing it. Plain prose is returned unchanged.
		text = humanizeStructuredText(text)
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
			// A non-text block (image, resource, embedded record) is reported by
			// what it is plus its content, instead of a raw JSON dump the model
			// has to decode. "type" is the block kind the SDK reported.
			rendered := nonTextContentText(item)
			if rendered != "" {
				parts = append(parts, rendered)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// nonTextContentText describes a non-text content block in readable form.
func nonTextContentText(item sdkmcp.Content) string {
	kind := ""
	if raw, err := json.Marshal(item); err == nil {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			kind = strings.TrimSpace(envelope.Type)
		}
		// Render the block's own fields as readable lines.
		var decoded map[string]any
		if json.Unmarshal(raw, &decoded) == nil {
			delete(decoded, "type")
			body := structuredToText(decoded)
			if strings.TrimSpace(body) != "" {
				if kind != "" {
					return "[" + kind + " content]\n" + body
				}
				return body
			}
		}
		if kind != "" {
			return "[" + kind + " content]"
		}
		return string(raw)
	}
	return ""
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
	case config.MCPTransportStreamableHTTP:
		if strings.TrimSpace(server.URL) == "" {
			return fmt.Errorf("mcp server %q requires url for %s transport", server.Name, config.MCPTransportStreamableHTTP)
		}
		if strings.TrimSpace(server.Command) != "" {
			return fmt.Errorf("mcp server %q %s transport must not set command", server.Name, config.MCPTransportStreamableHTTP)
		}
		return nil
	default:
		return fmt.Errorf("mcp server %q transport %q is not implemented yet", server.Name, server.Transport)
	}
}

func normalizeTransport(value string) string {
	return config.NormalizeMCPTransport(value)
}
