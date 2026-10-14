package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

func TestACPMCPParsing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       string
		transport string
		wantErr   string
	}{
		{"stdio array env", `[{"name":"test","command":"node","args":["server.js"],"env":[{"name":"TOKEN","value":"secret"}]}]`, "stdio", ""},
		{"stdio object env", `[{"name":"test","type":"stdio","command":"node","env":{"TOKEN":"secret"}}]`, "stdio", ""},
		{"http array headers", `[{"name":"test","type":"http","url":"https://example.com/mcp","headers":[{"name":"Authorization","value":"secret"}]}]`, config.MCPTransportStreamableHTTP, ""},
		{"http object headers", `[{"name":"test","type":"http","url":"https://example.com/mcp","headers":{"Authorization":"secret"}}]`, config.MCPTransportStreamableHTTP, ""},
		{"missing name", `[{"command":"node"}]`, "", "name is required"},
		{"duplicate names", `[{"name":"test","command":"node"},{"name":" test ","command":"node"}]`, "", "duplicate"},
		{"missing command", `[{"name":"test"}]`, "", "requires command"},
		{"stdio with url", `[{"name":"test","command":"node","url":"https://example.com/mcp"}]`, "", "must not set"},
		{"stdio with headers", `[{"name":"test","command":"node","headers":{"Authorization":"secret"}}]`, "", "must not set"},
		{"missing url", `[{"name":"test","type":"http"}]`, "", "absolute http"},
		{"relative url", `[{"name":"test","type":"http","url":"/mcp"}]`, "", "absolute http"},
		{"invalid scheme", `[{"name":"test","type":"http","url":"file:///tmp/mcp"}]`, "", "absolute http"},
		{"http with command", `[{"name":"test","type":"http","url":"https://example.com/mcp","command":"node"}]`, "", "must not set"},
		{"http with env", `[{"name":"test","type":"http","url":"https://example.com/mcp","env":{"TOKEN":"secret"}}]`, "", "must not set"},
		{"sse", `[{"name":"test","type":"sse","url":"https://example.com/sse"}]`, "", "SSE transport is not supported"},
		{"unknown type", `[{"name":"test","type":"websocket"}]`, "", "unsupported transport"},
		{"invalid env", `[{"name":"test","command":"node","env":42}]`, "", "env must be"},
		{"empty env name", `[{"name":"test","command":"node","env":[{"name":" ","value":"secret"}]}]`, "", "env entry name"},
		{"empty object env name", `[{"name":"test","command":"node","env":{"":"secret"}}]`, "", "env entry name"},
		{"invalid headers", `[{"name":"test","type":"http","headers":42}]`, "", "headers must be"},
		{"empty header name", `[{"name":"test","type":"http","headers":[{"name":"","value":"secret"}]}]`, "", "headers entry name"},
		{"empty object header name", `[{"name":"test","type":"http","headers":{" ":"secret"}}]`, "", "headers entry name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var specs []MCPServerSpec
			err := json.Unmarshal([]byte(tc.raw), &specs)
			var servers []config.MCPServerConfig
			if err == nil {
				servers, err = parseMCPServers(specs)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || len(servers) != 1 {
				t.Fatalf("parse = %+v, %v", servers, err)
			}
			server := servers[0]
			if server.Transport != tc.transport || server.Type != tc.transport {
				t.Fatalf("transport = %q, type = %q", server.Transport, server.Type)
			}
			if tc.transport == "stdio" && server.Env["TOKEN"] != "secret" {
				t.Fatalf("env = %v", server.Env)
			}
			if tc.transport != "stdio" && server.Headers["Authorization"] != "secret" {
				t.Fatalf("headers = %v", server.Headers)
			}
		})
	}
}

func TestACPMCPMergeIsolatedAndPersistenceFiltered(t *testing.T) {
	base := []config.MCPServerConfig{
		{Name: "shared", Command: "local", Disabled: true, Env: map[string]string{"TOKEN": "local"}},
		{Name: "local", Command: "local", Args: []string{"original"}, Env: map[string]string{"LOCAL": "original"}, Headers: map[string]string{"X-Local": "original"}},
	}
	client := []config.MCPServerConfig{
		{Name: "shared", Command: "client", Env: map[string]string{"TOKEN": "client"}},
		{Name: "remote", Transport: config.MCPTransportStreamableHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "secret"}},
	}
	merged := mergeMCPServers(base, client)
	if len(merged) != 3 || merged[0].Command != "client" || merged[0].Disabled {
		t.Fatalf("merge = %+v", merged)
	}
	merged[0].Env["TOKEN"] = "changed"
	merged[1].Args[0] = "changed"
	merged[1].Env["LOCAL"] = "changed"
	merged[1].Headers["X-Local"] = "changed"
	merged[2].Headers["Authorization"] = "changed"
	if base[1].Args[0] != "original" || base[1].Env["LOCAL"] != "original" || base[1].Headers["X-Local"] != "original" || client[0].Env["TOKEN"] != "client" || client[1].Headers["Authorization"] != "secret" {
		t.Fatal("merge mutated source configuration")
	}
	sess := &acpSession{clientMCPNames: map[string]bool{"shared": true, "remote": true}, baseMCPServers: base}
	merged[1].Disabled = true
	persisted := sess.persistentMCPServers(merged)
	if len(persisted) != 2 || persisted[0].Name != "local" || !persisted[0].Disabled || !reflect.DeepEqual(persisted[1], base[0]) {
		t.Fatalf("persistent config = %+v", persisted)
	}
	persisted[1].Env["TOKEN"] = "changed"
	if base[0].Env["TOKEN"] != "local" {
		t.Fatal("persistent configuration aliases base credentials")
	}
}

func newACPMCPTestServer(token string) *sdkmcp.Server {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "acp-test", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "echo", Description: "Echo test input"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, input struct {
			Text string `json:"text"`
		}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: token + ":" + input.Text}}}, nil, nil
		})
	return server
}

// The test binary is also a real stdio MCP subprocess; do not print test output
// to stdout, which is reserved for MCP JSON-RPC messages.
func TestACPMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("SOLCODE_ACP_MCP_HELPER") != "1" {
		return
	}
	err := newACPMCPTestServer(os.Getenv("ACP_MCP_TOKEN")).Run(context.Background(), &sdkmcp.StdioTransport{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestACPMCPNewAndLoadRegisterAndInvokeTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, method := range []string{MethodSessionNew, MethodSessionLoad} {
		for _, transport := range []string{"stdio", "http"} {
			t.Run(method+"/"+transport, func(t *testing.T) {
				spec := map[string]any{"name": "client"}
				if transport == "stdio" {
					executable, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					spec["command"] = executable
					spec["args"] = []string{"-test.run=^TestACPMCPStdioHelperProcess$"}
					spec["env"] = []map[string]string{{"name": "SOLCODE_ACP_MCP_HELPER", "value": "1"}, {"name": "ACP_MCP_TOKEN", "value": "acp-token"}}
				} else {
					mcpServer := newACPMCPTestServer("acp-token")
					handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return mcpServer }, nil)
					httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Authorization") != "Bearer acp-token" {
							http.Error(w, "missing ACP header", http.StatusUnauthorized)
							return
						}
						handler.ServeHTTP(w, r)
					}))
					t.Cleanup(httpServer.Close)
					spec["type"] = "http"
					spec["url"] = httpServer.URL
					spec["headers"] = []map[string]string{{"name": "Authorization", "value": "Bearer acp-token"}}
				}

				workDir, sessionDir := t.TempDir(), t.TempDir()
				if method == MethodSessionLoad {
					stored := session.NewSession("loaded-mcp", workDir, "test-model")
					if err := session.NewFileStore(sessionDir).Save(context.Background(), stored); err != nil {
						t.Fatal(err)
					}
				}
				client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
					cfg.WorkDir, cfg.Session.Dir = workDir, sessionDir
					cfg.MCP.Servers = []config.MCPServerConfig{{Name: "client", Command: "local-command", Disabled: true}, {Name: "local", Command: "unused", Disabled: true}}
					cfg.MCPServers = cloneMCPServers(cfg.MCP.Servers)
					return cfg
				}, nil)
				var initialized InitializeResult
				if err := json.Unmarshal(mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1}), &initialized); err != nil {
					t.Fatal(err)
				}
				if !initialized.AgentCapabilities.MCPCapabilities.HTTP || initialized.AgentCapabilities.MCPCapabilities.SSE {
					t.Fatalf("MCP capabilities = %+v", initialized.AgentCapabilities.MCPCapabilities)
				}
				params := map[string]any{"cwd": workDir, "mcpServers": []any{spec}}
				id := "loaded-mcp"
				if method == MethodSessionLoad {
					params["sessionId"] = id
				}
				raw := mustCall(t, client, method, params)
				if method == MethodSessionNew {
					var result SessionNewResult
					if err := json.Unmarshal(raw, &result); err != nil {
						t.Fatal(err)
					}
					id = result.SessionID
				}
				sess := client.server.getSession(id)
				if sess == nil {
					t.Fatal("session not created")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := sess.application.EnsureMCPTools(ctx); err != nil {
					t.Fatal(err)
				}
				mcpTool := sess.application.Tools.Find("mcp__client__echo")
				if mcpTool == nil {
					t.Fatal("ACP MCP tool was not registered")
				}
				result, err := mcpTool.Invoke(ctx, nil, json.RawMessage(`{"text":"hello"}`))
				if err != nil || result == nil || result.IsError || result.Text != "acp-token:hello" {
					t.Fatalf("MCP invocation = %+v, %v", result, err)
				}
				if !client.server.cfg.MCP.Servers[0].Disabled || client.server.cfg.MCP.Servers[0].Command != "local-command" {
					t.Fatal("ACP configuration mutated server defaults")
				}
				// A second session must see local defaults, not the first client's servers.
				var second SessionNewResult
				if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": workDir}), &second); err != nil {
					t.Fatal(err)
				}
				// Session bootstrap continues after session/new replies. Use a
				// synchronous request as a barrier before directly mutating state.
				mustCall(t, client, MethodAuthenticate, map[string]any{})
				other := client.server.getSession(second.SessionID)
				if !reflect.DeepEqual(other.cfg.MCP.Servers, client.server.cfg.MCP.Servers) {
					t.Fatalf("MCP config leaked into second session: %+v", other.cfg.MCP.Servers)
				}

				settingsPath := filepath.Join(t.TempDir(), "settings.json")
				msg, err := client.server.toggleMCP(sess, "client", settingsPath)
				if err != nil || !strings.Contains(msg, "current ACP session only") {
					t.Fatalf("toggle client = %q, %v", msg, err)
				}
				if _, err := os.Stat(settingsPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("client toggle persisted configuration: %v", err)
				}
			})
		}
	}
}

func TestACPMCPLocalToggleDoesNotPersistClientCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.MCP.Servers = []config.MCPServerConfig{
			{Name: "shared", Command: "local-command", Disabled: true, Env: map[string]string{"TOKEN": "local-token"}},
			{Name: "local", Command: "unused"},
		}
		cfg.MCPServers = cloneMCPServers(cfg.MCP.Servers)
		return cfg
	}, nil)
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	raw := mustCall(t, client, MethodSessionNew, map[string]any{
		"cwd": t.TempDir(),
		"mcpServers": []any{
			map[string]any{"name": "shared", "command": "client-command", "env": map[string]string{"TOKEN": "client-secret"}},
			map[string]any{"name": "remote", "type": "http", "url": "https://example.com/mcp", "headers": map[string]string{"Authorization": "client-secret"}},
		},
	})
	var created SessionNewResult
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	// Wait for the post-response bootstrap before directly toggling config.
	mustCall(t, client, MethodAuthenticate, map[string]any{})
	sess := client.server.getSession(created.SessionID)
	path := filepath.Join(t.TempDir(), "settings.json")
	msg, err := client.server.toggleMCP(sess, "local", path)
	if err != nil || strings.Contains(msg, "Warning:") || !strings.Contains(msg, "Disabled MCP server local") {
		t.Fatalf("toggle local = %q, %v", msg, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "client-secret") || strings.Contains(string(data), "client-command") || strings.Contains(string(data), "remote") {
		t.Fatalf("client configuration was persisted: %s", data)
	}
	var saved struct {
		MCP config.MCPConfig `json:"mcp"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.MCP.Servers) != 2 {
		t.Fatalf("saved MCP servers = %+v", saved.MCP.Servers)
	}
	for _, server := range saved.MCP.Servers {
		if !server.Disabled {
			t.Fatalf("saved server %q should be disabled", server.Name)
		}
		if server.Name == "shared" && (server.Command != "local-command" || server.Env["TOKEN"] != "local-token") {
			t.Fatalf("shadowed local configuration was not preserved: %+v", server)
		}
	}
}

func TestACPMCPInvalidParamsDoNotCreateSession(t *testing.T) {
	client := newACPHarness(t, nil)
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	for _, method := range []string{MethodSessionNew, MethodSessionLoad} {
		for _, spec := range []map[string]any{
			{"name": "bad", "type": "sse", "url": "https://example.com/sse"},
			{"name": "bad", "type": "http", "url": "/relative"},
			{"name": "bad"},
			{"name": "bad", "command": "node", "env": 42},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var result json.RawMessage
			err := client.conn.Call(ctx, method, map[string]any{"sessionId": "invalid", "cwd": t.TempDir(), "mcpServers": []any{spec}}, &result)
			cancel()
			var rpcErr *jsonrpcError
			if !errors.As(err, &rpcErr) || rpcErr.Code != JSONRPCInvalidParams {
				t.Fatalf("%s error = %v, want invalid params", method, err)
			}
		}
	}
	client.server.mu.Lock()
	defer client.server.mu.Unlock()
	if len(client.server.sessions) != 0 {
		t.Fatal("invalid MCP parameters created a session")
	}
}
