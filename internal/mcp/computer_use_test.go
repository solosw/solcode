package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

type nativeTestClient struct {
	mu                           sync.Mutex
	lifetime                     context.Context
	starts, lists, calls, closes int
	startErr                     error
	blockStart                   bool
}

func (f *nativeTestClient) Start(ctx context.Context) error {
	f.mu.Lock()
	f.starts++
	f.lifetime = ctx
	block, err := f.blockStart, f.startErr
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}
func (f *nativeTestClient) ListTools(context.Context) ([]tool.MCPToolInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return []tool.MCPToolInfo{{ToolName: "get_app_state"}, {ToolName: "click"}}, nil
}
func (f *nativeTestClient) CallTool(ctx context.Context, _ string, _ json.RawMessage) (*tool.ContentBlock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := f.lifetime.Err(); err != nil {
		return nil, fmt.Errorf("sidecar lifetime: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return tool.Result("ok"), nil
}
func (f *nativeTestClient) Close() error { f.mu.Lock(); defer f.mu.Unlock(); f.closes++; return nil }

func TestComputerUseSidecarIsLazyAndPersistsAcrossCalls(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := &nativeTestClient{}
	client := NewComputerUseClient(binary, t.TempDir(), func(server config.MCPServerConfig) Client {
		if server.Command != binary || len(server.Args) != 1 || server.Args[0] != "mcp" {
			t.Errorf("incorrect server config: %+v", server)
		}
		return fake
	})
	if fake.starts != 0 {
		t.Fatal("sidecar started eagerly")
	}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		result, err := client.CallTool(ctx, "get_app_state", json.RawMessage(`{"app":"Editor"}`))
		cancel()
		if err != nil || result.IsError {
			t.Fatalf("call %d: %+v, %v", i, result, err)
		}
	}
	if fake.starts != 1 || fake.lists != 1 || fake.calls != 2 {
		t.Fatalf("state was not reused: %+v", fake)
	}
	if fake.lifetime.Err() != nil {
		t.Fatal("single-call cancellation killed the persistent runtime")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.lifetime.Err() == nil || fake.closes != 1 {
		t.Fatal("sidecar was not closed")
	}
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Fatal("closed sidecar restarted")
	}
}
func TestComputerUseStartupCancellationAndFailureAreRetryable(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, blocked := range []bool{false, true} {
		fake := &nativeTestClient{blockStart: blocked}
		if !blocked {
			fake.startErr = fmt.Errorf("startup failed")
		}
		client := NewComputerUseClient(binary, t.TempDir(), func(config.MCPServerConfig) Client { return fake })
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		_, err := client.ListTools(ctx)
		cancel()
		if err == nil {
			t.Fatal("expected startup error")
		}
		if fake.closes != 1 || fake.lifetime.Err() == nil {
			t.Fatal("failed startup leaked process")
		}
		fake.mu.Lock()
		fake.blockStart = false
		fake.startErr = nil
		fake.mu.Unlock()
		if _, err := client.ListTools(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
	}
}
func TestComputerUseUnknownNativeToolDoesNotInvokeDesktop(t *testing.T) {
	binary, _ := os.Executable()
	fake := &nativeTestClient{}
	client := NewComputerUseClient(binary, t.TempDir(), func(config.MCPServerConfig) Client { return fake })
	defer client.Close()
	result, err := client.CallTool(context.Background(), "query", json.RawMessage(`{}`))
	if err != nil || !result.IsError || fake.calls != 0 {
		t.Fatalf("got=%+v err=%v", result, err)
	}
}
