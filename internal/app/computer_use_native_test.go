package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/mcp"
	"github.com/solosw/solcode/internal/tool"
)

func TestComputerUseLegacyRegistrationIsDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.ComputerUse.Enabled = true
	registry, skills, mcpRegistry, lsp, err := buildToolState(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpRegistry.Close()
	if lsp != nil {
		defer lsp.Close()
	}
	if registry.Find(tool.ComputerUseToolName) != nil {
		t.Fatal("old enabled-only configuration activated desktop tool")
	}
	if _, ok := skills.Find("computer-use"); ok {
		t.Fatal("disabled legacy tool still injected its skill")
	}
	// Also cover basic builtin registration: the old tool is never registered.
	direct := tool.NewRegistry()
	registerBuiltins(direct, nil, cfg.Sandbox, cfg.Image, false)
	if direct.Find(tool.ComputerUseToolName) != nil {
		t.Fatal("legacy registration bypassed migration gate")
	}
}
func TestNativeComputerUseRegistrationDefersProcessAndKeepsGuardrail(t *testing.T) {
	cfg := config.Default()
	cfg.ComputerUse = config.ComputerUseConfig{Enabled: true, Backend: config.ComputerUseBackendNative}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ComputerUse.Binary = binary
	fake := &startupMCPClient{}
	registry, skills, mcpRegistry, lsp, err := buildToolState(cfg, func(config.MCPServerConfig) mcp.Client { return fake })
	if err != nil {
		t.Fatal(err)
	}
	defer mcpRegistry.Close()
	defer closeBuiltinRuntimes(registry)
	if lsp != nil {
		defer lsp.Close()
	}
	if start, list := fake.calls(); start != 0 || list != 0 {
		t.Fatal("registration started desktop process")
	}
	native := registry.Find(tool.ComputerUseToolName)
	if native == nil {
		t.Fatal("native tool missing")
	}
	if _, ok := skills.Find("computer-use"); !ok {
		t.Fatal("native skill missing")
	}
	if !guardedTools[native.Name()] {
		t.Fatal("native tool bypasses Jev")
	}
	input := json.RawMessage(`{"action":"type_text","arguments":{"app":"Editor","text":"sensitive"}}`)
	if got := guardrailPayload(native.Name(), input); got != string(input) {
		t.Fatal("native payload was not checked")
	}
	result, err := native.Invoke(context.Background(), nil, json.RawMessage(`{"action":"describe"}`))
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if start, list := fake.calls(); start != 1 || list != 1 {
		t.Fatal("first invocation did not start sidecar")
	}
}

type trackedDesktopClient struct {
	startupMCPClient
	closes int
}

func (c *trackedDesktopClient) Close() error { c.closes++; return nil }

func TestComputerUseReloadAndExitCloseNativeProcess(t *testing.T) {
	cfg := config.Default()
	codeGraphDisabled := false
	cfg.CodeGraph.Enabled = &codeGraphDisabled
	cfg.WorkDir = t.TempDir()
	cfg.Memory.Enabled = false
	cfg.Session.Enabled = false
	cfg.KnowledgeGraph.Enabled = false
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ComputerUse = config.ComputerUseConfig{Enabled: true, Backend: config.ComputerUseBackendNative, Binary: binary}
	fake := &trackedDesktopClient{}
	application, err := New(cfg, WithMCPClientFactory(func(config.MCPServerConfig) mcp.Client { return fake }))
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	old := application.Tools.Find(tool.ComputerUseToolName)
	if _, err := old.Invoke(context.Background(), nil, json.RawMessage(`{"action":"describe"}`)); err != nil {
		t.Fatal(err)
	}
	cfg.ComputerUse.Enabled = false
	if err := application.ReloadFeatures(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if fake.closes != 1 || application.Tools.Find(tool.ComputerUseToolName) != nil {
		t.Fatal("reload left the old desktop process/tool active")
	}
	if result, _ := old.Invoke(context.Background(), nil, json.RawMessage(`{"action":"describe"}`)); !result.IsError {
		t.Fatal("stale tool handle restarted the old process")
	}
	cfg.ComputerUse.Enabled = true
	if err := application.ReloadFeatures(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Tools.Find(tool.ComputerUseToolName).Invoke(context.Background(), nil, json.RawMessage(`{"action":"describe"}`)); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.closes != 2 {
		t.Fatal("application exit leaked native process")
	}
}

func TestMissingNativeComputerUseBinaryDoesNotBlockAppStartup(t *testing.T) {
	cfg := config.Default()
	codeGraphDisabled := false
	cfg.CodeGraph.Enabled = &codeGraphDisabled
	cfg.WorkDir = t.TempDir()
	cfg.Memory.Enabled = false
	cfg.Session.Enabled = false
	cfg.KnowledgeGraph.Enabled = false
	cfg.ComputerUse = config.ComputerUseConfig{Enabled: true, Backend: config.ComputerUseBackendNative, Binary: cfg.WorkDir + "/missing-computer-use"}
	application, err := New(cfg)
	if err != nil {
		t.Fatalf("missing optional binary broke startup: %v", err)
	}
	defer application.Close()
	if err := application.EnsureMCPTools(context.Background()); err != nil {
		t.Fatalf("normal MCP startup touched native runtime: %v", err)
	}
	result, err := application.Tools.Find(tool.ComputerUseToolName).Invoke(context.Background(), nil, json.RawMessage(`{"action":"describe"}`))
	if err != nil || !result.IsError {
		t.Fatalf("missing runtime should be a tool error: result=%+v err=%v", result, err)
	}
}
