package unit_tests

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/lsp"
	"github.com/solosw/solcode/internal/tool"
)

func TestLSPValidateRequiresQueryForWorkspaceSymbol(t *testing.T) {
	err := lsp.Validate(lsp.Request{Operation: lsp.OperationWorkspaceSymbol})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLSPToolIsReadOnlyAndConcurrencySafe(t *testing.T) {
	lspTool := tool.NewLSPTool(lsp.NewManager(nil, nil))
	if !lspTool.IsReadOnly(nil) {
		t.Fatal("expected LSP tool to be read-only")
	}
	if !lspTool.IsConcurrencySafe(nil) {
		t.Fatal("expected LSP tool to be concurrency-safe")
	}
	if lspTool.IsDestructive(nil) {
		t.Fatal("LSP tool should not be destructive")
	}
}

func TestLSPToolValidateInput(t *testing.T) {
	lspTool := tool.NewLSPTool(lsp.NewManager(nil, nil))
	input, _ := json.Marshal(map[string]any{
		"operation": "hover",
		"file_path": "main.go",
		"line":      1,
		"character": 1,
	})
	if err := lspTool.ValidateInput(context.Background(), input); err != nil {
		t.Fatalf("expected valid input: %v", err)
	}
}

func TestLSPConfigNormalizeAndDefaults(t *testing.T) {
	cfg := config.Default()
	cfg.LSP = config.LSPConfig{
		Servers: []config.LSPServerConfig{
			{Language: "go", Extensions: []string{"go"}, Command: []string{"gopls"}},
			{Language: "skip", Extensions: []string{".py"}, Command: []string{"pyright-langserver"}, Disabled: true},
		},
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !cfg.LSPEnabled() {
		t.Fatal("expected LSP enabled by default")
	}
	if !cfg.LSPIncludeDefaults() {
		t.Fatal("expected include_defaults true by default")
	}
	if len(cfg.LSP.Servers) != 1 {
		t.Fatalf("servers = %#v", cfg.LSP.Servers)
	}
	if cfg.LSP.Servers[0].Extensions[0] != ".go" {
		t.Fatalf("extension normalize = %#v", cfg.LSP.Servers[0].Extensions)
	}
}
