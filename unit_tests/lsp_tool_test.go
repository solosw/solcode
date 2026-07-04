package unit_tests

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/solosw/codeplus-agent/internal/lsp"
	"github.com/solosw/codeplus-agent/internal/tool"
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
