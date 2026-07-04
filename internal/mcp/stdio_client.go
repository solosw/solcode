package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/tool"
)

type stdioClient struct {
	server config.MCPServerConfig

	mu      sync.Mutex
	client  *sdkmcp.Client
	session *sdkmcp.ClientSession
}

func NewStdioClient(server config.MCPServerConfig) Client {
	return &stdioClient{server: server}
}

func (c *stdioClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := validateServerConfig(c.server); err != nil {
		return err
	}
	if c.session != nil {
		return nil
	}

	execCmd := exec.CommandContext(ctx, c.server.Command, c.server.Args...)
	execCmd.Env = os.Environ()
	for key, value := range c.server.Env {
		execCmd.Env = append(execCmd.Env, key+"="+value)
	}
	if c.server.URL != "" {
		return fmt.Errorf("mcp server %q transport stdio does not use url", c.server.Name)
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "codeplus-agent", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.CommandTransport{Command: execCmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		return fmt.Errorf("connect mcp server %q: %w", c.server.Name, err)
	}

	c.client = client
	c.session = session
	return nil
}

func (c *stdioClient) ListTools(ctx context.Context) ([]tool.MCPToolInfo, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("mcp server %q is not started", c.server.Name)
	}
	result, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	tools := make([]tool.MCPToolInfo, 0, len(result.Tools))
	for _, sdkTool := range result.Tools {
		tools = append(tools, toolInfoFromSDK(c.server.Name, sdkTool))
	}
	return tools, nil
}

func (c *stdioClient) CallTool(ctx context.Context, toolName string, input json.RawMessage) (*tool.ContentBlock, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("mcp server %q is not started", c.server.Name)
	}
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

func (c *stdioClient) Close() error {
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
