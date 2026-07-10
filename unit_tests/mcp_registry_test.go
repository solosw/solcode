package unit_tests

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/solosw/solcode/internal/config"
	internalmcp "github.com/solosw/solcode/internal/mcp"
	"github.com/solosw/solcode/internal/tool"
)

type fakeMCPClient struct {
	startErr error
	listErr  error
	callErr  error
	closed   bool
	calls    []string
	inputs   []map[string]any
	tools    []fakeToolDef
}

type fakeToolDef struct {
	name        string
	description string
	schema      map[string]any
	result      string
}

func (f *fakeMCPClient) Start(ctx context.Context) error {
	_ = ctx
	return f.startErr
}

func (f *fakeMCPClient) ListTools(ctx context.Context) ([]tool.MCPToolInfo, error) {
	_ = ctx
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]tool.MCPToolInfo, 0, len(f.tools))
	for _, item := range f.tools {
		out = append(out, tool.MCPToolInfo{ToolName: item.name, Description: item.description, InputSchema: item.schema})
	}
	return out, nil
}

func (f *fakeMCPClient) CallTool(ctx context.Context, toolName string, input json.RawMessage) (*tool.ContentBlock, error) {
	_ = ctx
	if f.callErr != nil {
		return nil, f.callErr
	}
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, toolName)
	f.inputs = append(f.inputs, args)
	for _, item := range f.tools {
		if item.name == toolName {
			return tool.Result(item.result), nil
		}
	}
	return tool.ErrorResult("unknown fake tool"), nil
}

func (f *fakeMCPClient) Close() error {
	f.closed = true
	return nil
}

func TestMCPRegistryRegistersDynamicTools(t *testing.T) {
	fake := &fakeMCPClient{tools: []fakeToolDef{{name: "read_file", description: "Read file", schema: map[string]any{"type": "object"}, result: "ok"}}}
	registry := internalmcp.NewRegistry([]config.MCPServerConfig{{Name: "filesystem", Transport: "stdio", Command: "npx"}})
	registry.SetClientFactory(func(server config.MCPServerConfig) internalmcp.Client {
		return fake
	})
	if err := registry.Load(); err != nil {
		t.Fatalf("Load() = %v", err)
	}
	tools := registry.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "mcp__filesystem__read-file" {
		t.Fatalf("unexpected tool name: %s", tools[0].Name())
	}
	result, err := tools[0].Invoke(context.Background(), nil, json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatalf("Invoke() = %v", err)
	}
	if result.Text != "ok" {
		t.Fatalf("unexpected result: %q", result.Text)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "read_file" {
		t.Fatalf("calls = %#v", fake.calls)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if !fake.closed {
		t.Fatal("expected fake client to be closed")
	}
}

func TestMCPRegistrySkipsDisabledServers(t *testing.T) {
	registry := internalmcp.NewRegistry([]config.MCPServerConfig{{Name: "filesystem", Disabled: true, Transport: "stdio", Command: "npx"}})
	registry.SetClientFactory(func(server config.MCPServerConfig) internalmcp.Client {
		t.Fatal("factory should not be called for disabled server")
		return nil
	})
	if err := registry.Load(); err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(registry.Tools()) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(registry.Tools()))
	}
}

func TestMCPRegistryReturnsStartupErrors(t *testing.T) {
	registry := internalmcp.NewRegistry([]config.MCPServerConfig{{Name: "filesystem", Transport: "stdio", Command: "npx"}})
	registry.SetClientFactory(func(server config.MCPServerConfig) internalmcp.Client {
		return &fakeMCPClient{startErr: errors.New("boom")}
	})
	if err := registry.Load(); err == nil {
		t.Fatal("expected startup error, got nil")
	}
}

func TestMCPRegistryAcceptsSSEAndHTTPConfigs(t *testing.T) {
	for _, server := range []config.MCPServerConfig{
		{Name: "remote-sse", Transport: "sse", URL: "https://example.com/sse"},
		{Name: "remote-http", Transport: "http", URL: "https://example.com/mcp"},
	} {
		fake := &fakeMCPClient{tools: []fakeToolDef{{name: "ping", description: "Ping", result: "pong"}}}
		registry := internalmcp.NewRegistry([]config.MCPServerConfig{server})
		registry.SetClientFactory(func(server config.MCPServerConfig) internalmcp.Client { return fake })
		if err := registry.Load(); err != nil {
			t.Fatalf("Load(%q) = %v", server.Transport, err)
		}
		tools := registry.Tools()
		if len(tools) != 1 {
			t.Fatalf("transport %q expected 1 tool, got %d", server.Transport, len(tools))
		}
		if err := registry.Close(); err != nil {
			t.Fatalf("Close(%q) = %v", server.Transport, err)
		}
	}
}
