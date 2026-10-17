package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/solosw/solcode/internal/config"
)

// Helper process exercises real stdio framing, MCP negotiation, image decoding,
// process lifetime and shutdown without accessing the user's desktop.
func TestComputerUseMCPProcess(t *testing.T) {
	if os.Getenv("SOLCODE_MCP_DESKTOP_TEST_HELPER") != "1" {
		return
	}
	calls := 0
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fake-native-desktop", Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "get_app_state", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls++
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("snapshot %d", calls)}, &sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte("fake-screenshot")}}}, nil
	})
	server.AddTool(&sdkmcp.Tool{Name: "click", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		os.Exit(3) // Simulates a crash after an action may already have happened.
		return nil, nil
	})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestComputerUseRealStdioLifecycleAndCrashRecovery(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	client := NewComputerUseClient(executable, t.TempDir(), func(server config.MCPServerConfig) Client {
		starts++
		server.Args = []string{"-test.run=^TestComputerUseMCPProcess$"}
		server.Env = map[string]string{"SOLCODE_MCP_DESKTOP_TEST_HELPER": "1"}
		return NewStdioClient(server)
	})
	defer client.Close()
	for i := 1; i <= 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		result, err := client.CallTool(ctx, "get_app_state", json.RawMessage(`{"app":"fixture"}`))
		cancel()
		if err != nil || result.Type != "image" || !strings.Contains(result.Text, fmt.Sprintf("snapshot %d", i)) {
			t.Fatalf("call %d result=%+v err=%v", i, result, err)
		}
	}
	if starts != 1 {
		t.Fatal("stdio subprocess did not persist across calls")
	}
	if _, err := client.CallTool(context.Background(), "click", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("uncertain action did not fail closed: %v", err)
	}
	if starts != 1 {
		t.Fatal("mutating action was automatically retried")
	}
	result, err := client.CallTool(context.Background(), "get_app_state", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(result.Text, "snapshot 1") || starts != 2 {
		t.Fatalf("new session did not recover: %+v err=%v starts=%d", result, err, starts)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal("close must be idempotent:", err)
	}
}
