package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const CodeGraphToolName = "CodeGraph"

type CodeGraphBackend interface {
	Start()
	CallTool(context.Context, string, json.RawMessage) (*ContentBlock, error)
	Close() error
}
type codeGraphTool struct {
	BaseTool
	backend CodeGraphBackend
	root    string
}

// Only these upstream read-only operations are exposed; no SQL or management.
var codeGraphFields = map[string]string{
	"explore": "query maxFiles", "search": "query kind limit", "node": "symbol file includeCode offset limit symbolsOnly line",
	"callers": "symbol file limit", "callees": "symbol file limit", "impact": "symbol file depth",
	"files": "path pattern format includeMetadata maxDepth", "status": "",
}

func NewCodeGraphTool(backend CodeGraphBackend, root string) Tool {
	return &codeGraphTool{backend: backend, root: root}
}
func (t *codeGraphTool) Name() string { return CodeGraphToolName }
func (t *codeGraphTool) Description() string {
	return `Query the project's embedded CodeGraph semantic code index: explore relevant source and call paths, find symbols, callers/callees and refactoring impact. Prefer action=explore with query containing symbol/file names before broad Grep/View loops. Use node for a symbol or file, files for indexed layout, and status for health. The upstream runtime builds the index automatically in the background and watches code changes; if initializing/unavailable use Grep, View or LSP instead of polling. Results may lag edits: verify source before modifying. This tool only queries the current project; no SQL, management commands or projectPath overrides.`
}
func (t *codeGraphTool) InputSchema() map[string]any {
	props := map[string]any{"action": map[string]any{"type": "string", "enum": []string{"explore", "search", "node", "callers", "callees", "impact", "files", "status"}}}
	for key, description := range map[string]string{
		"query":  "Required for explore/search: symbol names, file names or short code terms.",
		"symbol": "Required for callers/callees/impact; node accepts symbol or file.",
		"file":   "node/callers/callees/impact: project-relative file path to read or disambiguate a symbol.",
		"path":   "files: project-relative directory filter.", "pattern": "files: glob filter, e.g. **/*.go.",
		"kind":   "search: function, method, class, interface, type, variable, route or component.",
		"format": "files: tree (default), flat or grouped.",
	} {
		props[key] = map[string]any{"type": "string", "description": description}
	}
	for key, description := range map[string]string{
		"maxFiles": "explore: maximum files (1-20).", "limit": "search/callers/callees: result limit; node: line count (up to 2000).",
		"depth": "impact: dependency traversal depth (1-10).", "offset": "node: 1-based start line in file mode.",
		"line": "node: disambiguate symbol at this 1-based line.", "maxDepth": "files: directory tree depth (1-10).",
	} {
		props[key] = map[string]any{"type": "integer", "minimum": 1, "description": description}
	}
	for _, key := range []string{"includeCode", "symbolsOnly", "includeMetadata"} {
		props[key] = map[string]any{"type": "boolean"}
	}
	return map[string]any{"type": "object", "properties": props, "required": []string{"action"}, "additionalProperties": false}
}
func (t *codeGraphTool) parse(input json.RawMessage) (string, map[string]json.RawMessage, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(input, &args); err != nil {
		return "", nil, err
	}
	var action string
	if err := json.Unmarshal(args["action"], &action); err != nil {
		return "", nil, fmt.Errorf("action is required")
	}
	fields, ok := codeGraphFields[action]
	if !ok {
		return "", nil, fmt.Errorf("unsupported CodeGraph action %q", action)
	}
	delete(args, "action")
	allowed := map[string]bool{}
	for _, f := range strings.Fields(fields) {
		allowed[f] = true
	}
	for key, raw := range args {
		if !allowed[key] {
			return "", nil, fmt.Errorf("%s is not allowed for %s", key, action)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return "", nil, fmt.Errorf("%s cannot be null", key)
		}
		switch key {
		case "maxFiles", "limit", "depth", "offset", "line", "maxDepth":
			var n int
			if err := json.Unmarshal(raw, &n); err != nil || n < 1 {
				return "", nil, fmt.Errorf("%s must be a positive integer", key)
			}
			max := 2000
			if key == "maxFiles" {
				max = 20
			}
			if key == "depth" || key == "maxDepth" {
				max = 10
			}
			if key == "offset" || key == "line" {
				max = 10000000
			}
			if n > max {
				return "", nil, fmt.Errorf("%s exceeds %d", key, max)
			}
		case "includeCode", "symbolsOnly", "includeMetadata":
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", nil, err
			}
		default:
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", nil, err
			}
			if key == "file" || key == "path" {
				if v != "" && (!filepath.IsLocal(v) || strings.Contains(v, "\\") || strings.Contains(v, ":")) {
					return "", nil, fmt.Errorf("%s must be a project-relative path", key)
				}
				if v != "" && t.root != "" {
					root, err := filepath.EvalSymlinks(t.root)
					if err != nil {
						return "", nil, err
					}
					if resolved, err := filepath.EvalSymlinks(filepath.Join(root, v)); err == nil {
						rel, err := filepath.Rel(root, resolved)
						if err != nil || !filepath.IsLocal(rel) {
							return "", nil, fmt.Errorf("%s escapes the project", key)
						}
					}
				}
			}
		}
	}
	required := ""
	switch action {
	case "explore", "search":
		required = "query"
	case "callers", "callees", "impact":
		required = "symbol"
	}
	if required != "" {
		var v string
		_ = json.Unmarshal(args[required], &v)
		if strings.TrimSpace(v) == "" {
			return "", nil, fmt.Errorf("%s is required", required)
		}
	}
	if action == "node" && args["symbol"] == nil && args["file"] == nil {
		return "", nil, fmt.Errorf("node requires symbol or file")
	}
	return action, args, nil
}
func (t *codeGraphTool) ValidateInput(_ context.Context, input json.RawMessage) error {
	_, _, err := t.parse(input)
	return err
}
func (t *codeGraphTool) Invoke(ctx context.Context, _ *UseContext, input json.RawMessage) (*ContentBlock, error) {
	action, args, err := t.parse(input)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return t.backend.CallTool(ctx, "codegraph_"+action, raw)
}
func (t *codeGraphTool) IsReadOnly(json.RawMessage) bool        { return true }
func (t *codeGraphTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (t *codeGraphTool) Start()                                 { t.backend.Start() }
func (t *codeGraphTool) Close() error                           { return t.backend.Close() }
