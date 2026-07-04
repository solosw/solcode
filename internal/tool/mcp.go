package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const MCPToolPrefix = "mcp__"

// MCPToolInfo holds metadata for an MCP-provided tool.
type MCPToolInfo struct {
	ServerName  string
	ToolName    string
	Description string
	InputSchema map[string]any
}

// MCPAdapter wraps an MCP tool to satisfy the Tool interface.
type MCPAdapter struct {
	BaseTool
	info     MCPToolInfo
	invokeFn func(ctx context.Context, input json.RawMessage) (*ContentBlock, error)
}

// NewMCPAdapter creates a new MCP tool adapter.
// invokeFn is the function that calls the actual MCP server tool.
func NewMCPAdapter(info MCPToolInfo, invokeFn func(ctx context.Context, input json.RawMessage) (*ContentBlock, error)) Tool {
	return &MCPAdapter{
		info:     info,
		invokeFn: invokeFn,
	}
}

func (m *MCPAdapter) Name() string {
	return MCPToolName(m.info.ServerName, m.info.ToolName)
}

func (m *MCPAdapter) Description() string {
	return fmt.Sprintf("[MCP: %s] %s", m.info.ServerName, m.info.Description)
}

func (m *MCPAdapter) InputSchema() map[string]any {
	if m.info.InputSchema != nil {
		return m.info.InputSchema
	}
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (m *MCPAdapter) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	if m.invokeFn == nil {
		return ErrorResult("MCP tool invoke function not set"), nil
	}
	return m.invokeFn(ctx, input)
}

// IsMCP reports true so the engine can identify MCP tools.
func (m *MCPAdapter) IsMCP() bool { return true }

// RegisterMCPTools creates MCPAdapter instances for a set of MCP server tools.
func RegisterMCPTools(serverName string, tools []MCPToolInfo, invokeFn func(toolName string) func(ctx context.Context, input json.RawMessage) (*ContentBlock, error)) []Tool {
	result := make([]Tool, 0, len(tools))
	for _, t := range tools {
		info := t
		info.ServerName = serverName
		fn := invokeFn(t.ToolName)
		result = append(result, NewMCPAdapter(info, fn))
	}
	return result
}

func MCPToolName(serverName, toolName string) string {
	return MCPToolPrefix + sanitizeMCPSegment(serverName) + "__" + sanitizeMCPSegment(toolName)
}

func sanitizeMCPSegment(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "unknown"
	}
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			lastDash = false
		case r == '-':
			if !lastDash {
				out.WriteRune(r)
				lastDash = true
			}
		default:
			if !lastDash {
				out.WriteRune('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(out.String(), "-")
	if result == "" {
		return "unknown"
	}
	return result
}
