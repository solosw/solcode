package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/solosw/codeplus-agent/internal/lsp"
)

type lspTool struct {
	BaseTool
	manager *lsp.Manager
}

func NewLSPTool(manager *lsp.Manager) Tool {
	return &lspTool{manager: manager}
}

func (t *lspTool) Name() string { return "LSP" }

func (t *lspTool) Description() string {
	return "Provides read-only Language Server Protocol operations such as document symbols, workspace symbols, go to definition, find references, hover, and go to implementation."
}

func (t *lspTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"description": "LSP operation to perform",
				"enum":        []string{"document_symbol", "workspace_symbol", "go_to_definition", "find_references", "hover", "go_to_implementation"},
			},
			"file_path": map[string]any{"type": "string", "description": "File path for file-position based operations"},
			"line":      map[string]any{"type": "integer", "description": "1-based line number"},
			"character": map[string]any{"type": "integer", "description": "1-based character offset"},
			"query":     map[string]any{"type": "string", "description": "Workspace symbol search query"},
		},
		"required": []string{"operation"},
	}
}

func (t *lspTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *lspTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *lspTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *lspTool) ValidateInput(_ context.Context, input json.RawMessage) error {
	var req lsp.Request
	if err := json.Unmarshal(input, &req); err != nil {
		return err
	}
	return lsp.Validate(req)
}

func (t *lspTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var req lsp.Request
	if err := json.Unmarshal(input, &req); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	if uctx != nil {
		req.WorkDir = uctx.WorkDir
	}
	if t.manager == nil {
		return ErrorResult("lsp manager is not configured"), nil
	}
	resp, err := t.manager.Execute(ctx, req)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal lsp response: %w", err)
	}
	return Result(string(data)), nil
}
