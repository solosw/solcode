package unit_tests

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/solosw/codeplus-agent/internal/tool"
)

type dummyTool struct {
	name        string
	readOnly    bool
	destructive bool
}

func (d *dummyTool) Name() string                                 { return d.name }
func (d *dummyTool) Description() string                           { return "dummy" }
func (d *dummyTool) InputSchema() map[string]any                   { return nil }
func (d *dummyTool) IsDestructive(_ json.RawMessage) bool          { return d.destructive }
func (d *dummyTool) IsReadOnly(_ json.RawMessage) bool             { return d.readOnly }
func (d *dummyTool) IsConcurrencySafe(_ json.RawMessage) bool      { return false }
func (d *dummyTool) Aliases() []string                             { return nil }
func (d *dummyTool) ValidateInput(_ context.Context, _ json.RawMessage) error { return nil }
func (d *dummyTool) Invoke(ctx context.Context, uctx *tool.UseContext, input json.RawMessage) (*tool.ContentBlock, error) {
	return tool.Result("ok"), nil
}

func TestRegistry_RegisterAndAll(t *testing.T) {
	reg := tool.NewRegistry()
	if reg.Len() != 0 {
		t.Fatal("expected empty registry")
	}
	reg.Register(&dummyTool{name: "toolB"}, &dummyTool{name: "toolA"})
	if reg.Len() != 2 {
		t.Fatalf("expected 2, got %d", reg.Len())
	}

	all := reg.All()
	if all[0].Name() != "toolA" || all[1].Name() != "toolB" {
		t.Fatalf("expected sorted [toolA, toolB], got [%s, %s]", all[0].Name(), all[1].Name())
	}
}

func TestRegistry_Find(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&dummyTool{name: "bash"})

	if reg.Find("bash") == nil {
		t.Fatal("expected to find bash tool")
	}
	if reg.Find("nonexistent") != nil {
		t.Fatal("expected nil for nonexistent tool")
	}
}

func TestRegistry_Filter(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(
		&dummyTool{name: "View", readOnly: true},
		&dummyTool{name: "Write", destructive: true},
		&dummyTool{name: "Glob", readOnly: true},
	)

	if reg.Filter(nil) != nil {
		t.Fatal("expected nil for nil whitelist")
	}
	if len(reg.Filter([]string{})) != 3 {
		t.Fatal("expected all 3 tools for empty whitelist")
	}
	if len(reg.Filter([]string{"View", "Write"})) != 2 {
		t.Fatal("expected 2 tools for specific filter")
	}
}

func TestResult(t *testing.T) {
	r := tool.Result("hello")
	if r.Text != "hello" || r.IsError {
		t.Fatal("Result helper wrong")
	}
}

func TestErrorResult(t *testing.T) {
	r := tool.ErrorResult("err")
	if r.Text != "err" || !r.IsError {
		t.Fatal("ErrorResult helper wrong")
	}
}

func TestMCPAdapter(t *testing.T) {
	adapter := tool.NewMCPAdapter(tool.MCPToolInfo{
		ServerName:  "test-server",
		ToolName:    "test-tool",
		Description: "A test MCP tool",
	}, func(ctx context.Context, input json.RawMessage) (*tool.ContentBlock, error) {
		return tool.Result("mcp result"), nil
	})

	if adapter.Name() != "mcp__test-server__test-tool" {
		t.Fatalf("unexpected MCP name: %s", adapter.Name())
	}

	uctx := &tool.UseContext{WorkDir: "/tmp"}
	result, err := adapter.Invoke(context.Background(), uctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "mcp result" {
		t.Fatalf("unexpected result: %s", result.Text)
	}
}
