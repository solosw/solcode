package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/tool"
)

type Registry struct {
	servers []config.MCPServerConfig
	tools   []tool.Tool
	factory ClientFactory

	mu      sync.Mutex
	clients map[string]Client
}

func NewRegistry(servers []config.MCPServerConfig) *Registry {
	return &Registry{
		servers: append([]config.MCPServerConfig(nil), servers...),
		factory: NewClientFactory(),
		clients: make(map[string]Client),
	}
}

func (r *Registry) SetClientFactory(factory ClientFactory) {
	if factory == nil {
		return
	}
	r.factory = factory
}

func (r *Registry) Load() error {
	return r.LoadContext(context.Background())
}

func (r *Registry) LoadContext(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.closeLocked(); err != nil {
		return err
	}
	r.tools = r.tools[:0]
	r.clients = make(map[string]Client)

	seenToolNames := make(map[string]string)
	for _, server := range r.servers {
		if server.Disabled || strings.TrimSpace(server.Name) == "" {
			continue
		}
		if err := validateServerConfig(server); err != nil {
			return err
		}

		client := r.factory(server)
		if client == nil {
			return fmt.Errorf("mcp server %q client factory returned nil", server.Name)
		}
		if err := client.Start(ctx); err != nil {
			_ = client.Close()
			return err
		}

		infos, err := client.ListTools(ctx)
		if err != nil {
			_ = client.Close()
			return fmt.Errorf("list tools for mcp server %q: %w", server.Name, err)
		}

		serverName := server.Name
		for _, info := range infos {
			qualified := tool.MCPToolName(serverName, info.ToolName)
			if prior, exists := seenToolNames[qualified]; exists {
				_ = client.Close()
				return fmt.Errorf("duplicate mcp tool name %q from servers %q and %q", qualified, prior, serverName)
			}
			seenToolNames[qualified] = serverName
		}

		mcpTools := tool.RegisterMCPTools(serverName, infos, func(toolName string) func(context.Context, json.RawMessage) (*tool.ContentBlock, error) {
			return func(ctx context.Context, input json.RawMessage) (*tool.ContentBlock, error) {
				return client.CallTool(ctx, toolName, input)
			}
		})
		r.tools = append(r.tools, mcpTools...)
		r.clients[serverName] = client
	}

	sort.SliceStable(r.tools, func(i, j int) bool { return r.tools[i].Name() < r.tools[j].Name() })
	return nil
}

func (r *Registry) Tools() []tool.Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tool.Tool(nil), r.tools...)
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeLocked()
}

func (r *Registry) closeLocked() error {
	var firstErr error
	for name, client := range r.clients {
		if client == nil {
			continue
		}
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close mcp server %q: %w", name, err)
		}
	}
	return firstErr
}
