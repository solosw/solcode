package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/codeplus-agent/internal/app"
	"github.com/solosw/codeplus-agent/internal/config"
	internalmcp "github.com/solosw/codeplus-agent/internal/mcp"
	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestAppNewRegistersSkillAndMCPTools(t *testing.T) {
	workDir := t.TempDir()
	skillsDir := filepath.Join(workDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "verify.md"), []byte("# verify\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	cfg := config.Default()
	cfg.WorkDir = workDir
	cfg.Skills.Paths = []string{skillsDir}
	cfg.MCP.Servers = []config.MCPServerConfig{{
		Name:      "filesystem",
		Transport: "stdio",
		Command:   "npx",
		Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
	}}

	fake := &fakeMCPClient{tools: []fakeToolDef{{name: "read_file", description: "Read file", schema: map[string]any{"type": "object"}, result: "content"}}}
	application, err := app.New(cfg, app.WithMCPClientFactory(func(server config.MCPServerConfig) internalmcp.Client {
		return fake
	}))
	if err != nil {
		t.Fatalf("app.New() = %v", err)
	}
	defer func() { _ = application.Close() }()

	if application.Tools.Find("Skill") == nil {
		t.Fatal("expected Skill tool to be registered")
	}
	mcpTool := application.Tools.Find("mcp__filesystem__read-file")
	if mcpTool == nil {
		t.Fatal("expected mcp__filesystem__read-file tool to be registered")
	}
	result, err := mcpTool.Invoke(context.Background(), &tool.UseContext{WorkDir: workDir}, json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatalf("mcp tool invoke: %v", err)
	}
	if result.Text != "content" {
		t.Fatalf("unexpected mcp result: %q", result.Text)
	}
	if len(application.SkillRegistry.All()) != 1 {
		t.Fatalf("expected 1 loaded skill, got %d", len(application.SkillRegistry.All()))
	}
}
