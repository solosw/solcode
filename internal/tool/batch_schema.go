package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// The wrapped tools are hidden on the wire, so their actual schemas must live
// here rather than forcing the model to guess argument names from examples.
func batchOperationSchema() map[string]any {
	tools := []Tool{NewViewTool(), NewGrepTool(), NewGlobTool(), NewLsTool(), NewEditTool(), NewMultiEditTool(), NewWriteTool(), NewMultiWriteTool(), NewPatchTool(), NewBashTool(), NewLSPTool(nil)}
	variants := make([]any, 0, len(tools))
	for _, child := range tools {
		inputSchema := child.InputSchema()
		inputSchema["additionalProperties"] = false
		variants = append(variants, map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"id":         map[string]any{"type": "string", "minLength": 1, "description": "Optional unique ID. Default op_1, op_2, ...; explicit IDs are recommended for dependencies."},
				"tool":       map[string]any{"type": "string", "enum": []string{child.Name()}},
				"input":      inputSchema,
				"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Ordering only; no result interpolation. Only use for known inputs."},
			}, "required": []string{"tool", "input"},
		})
	}
	return map[string]any{"anyOf": variants}
}

func validateBatchChild(ctx context.Context, child Tool, input json.RawMessage) error {
	if child == nil {
		return fmt.Errorf("tool unavailable")
	}
	data, err := json.Marshal(child.InputSchema())
	if err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return err
	}
	// Reject misspellings rather than allowing encoding/json to silently drop
	// e.g. file_path or new_text. Do not tighten arbitrary nested user objects.
	schema.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("resolve input schema: %w", err)
	}
	var args any
	if err := json.Unmarshal(input, &args); err != nil {
		return err
	}
	if err := resolved.Validate(args); err != nil {
		return err
	}
	return child.ValidateInput(ctx, input)
}
