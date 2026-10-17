package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/solosw/solcode/internal/computeruse/bundle"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

// ComputerUseClient is a lazy, session-scoped native MCP sidecar. It is not
// loaded with user MCP servers, so ACP startup and unrelated turns never launch
// a desktop process. A failed action is never replayed automatically.
type ComputerUseClient struct {
	mu               sync.Mutex
	binary, cacheDir string
	factory          ClientFactory
	client           Client
	cancel           context.CancelFunc
	tools            []tool.MCPToolInfo
	closed           bool
}

func NewComputerUseClient(binary, cacheDir string, factory ClientFactory) *ComputerUseClient {
	if factory == nil {
		factory = NewClientFactory()
	}
	return &ComputerUseClient{binary: binary, cacheDir: cacheDir, factory: factory}
}

func (c *ComputerUseClient) ensure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed {
		return fmt.Errorf("ComputerUse runtime is closed")
	}
	if c.client != nil {
		return nil
	}
	path, err := bundle.Resolve(ctx, c.binary, c.cacheDir)
	if err != nil {
		return err
	}
	client := c.factory(config.MCPServerConfig{Name: "open-computer-use", Transport: "stdio", Command: path, Args: []string{"mcp"}})
	if client == nil {
		return fmt.Errorf("ComputerUse client factory returned nil")
	}
	// The process lifetime must not be tied to a single tool call's deadline:
	// native element indices and application state persist across calls.
	lifetime, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- client.Start(lifetime) }()
	select {
	case err = <-started:
	case <-ctx.Done():
		cancel()
		// Start must honor its context (as the stdio transport does). Joining avoids
		// leaking a child started concurrently with cancellation.
		<-started
		_ = client.Close()
		return ctx.Err()
	}
	if err != nil {
		cancel()
		_ = client.Close()
		return fmt.Errorf("start ComputerUse: %w", err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		cancel()
		return fmt.Errorf("list ComputerUse tools: %w", err)
	}
	c.client, c.cancel, c.tools = client, cancel, tools
	return nil
}

func (c *ComputerUseClient) ListTools(ctx context.Context) ([]tool.MCPToolInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	return append([]tool.MCPToolInfo(nil), c.tools...), nil
}

func (c *ComputerUseClient) CallTool(ctx context.Context, name string, input json.RawMessage) (*tool.ContentBlock, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	for _, info := range c.tools {
		if info.ToolName == name {
			var result *tool.ContentBlock
			var err error
			if stdio, ok := c.client.(*stdioClient); ok {
				result, err = stdio.callTool(ctx, name, input)
			} else {
				result, err = c.client.CallTool(ctx, name, input)
			}
			if err != nil {
				// Transport loss or cancellation leaves action completion uncertain. Kill
				// this session, but NEVER retry the action; the next call starts afresh.
				_ = c.client.Close()
				c.cancel()
				c.client, c.cancel, c.tools = nil, nil, nil
				return nil, fmt.Errorf("ComputerUse session reset after transport failure: %w; do not replay the action blindly, get_app_state again before interacting", err)
			}
			return result, nil
		}
	}
	return tool.ErrorResult(fmt.Sprintf("native runtime does not support %q; use ComputerUse action=describe to inspect available tools", name)), nil
}

func (c *ComputerUseClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	// Close stdin gracefully first so the native runtime can restore parked
	// windows. Only cancel the process context after the transport has exited.
	var err error
	if c.client != nil {
		err = c.client.Close()
		c.client = nil
	}
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	return err
}
