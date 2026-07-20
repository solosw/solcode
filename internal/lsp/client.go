package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Client executes LSP tool requests against one or more language servers.
type Client interface {
	Request(ctx context.Context, req Request) (Response, error)
}

// ProcessClient spawns language servers and routes requests by file extension.
type ProcessClient struct {
	registry *Registry

	mu       sync.Mutex
	sessions map[string]*session // key: workDir + "\x00" + language
}

// NewProcessClient creates a client that manages language-server processes.
func NewProcessClient(registry *Registry) *ProcessClient {
	if registry == nil {
		registry = NewRegistry()
	}
	return &ProcessClient{
		registry: registry,
		sessions: make(map[string]*session),
	}
}

// Close shuts down all language-server sessions.
func (c *ProcessClient) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, s := range c.sessions {
		s.Close()
		delete(c.sessions, k)
	}
}

// Request implements Client.
func (c *ProcessClient) Request(ctx context.Context, req Request) (Response, error) {
	if c == nil {
		return Response{}, fmt.Errorf("%w for operation %s", ErrServerUnavailable, req.Operation)
	}
	cmd, workDir, err := c.resolveCommand(req)
	if err != nil {
		return Response{}, err
	}
	sess, err := c.getOrStart(ctx, cmd, workDir)
	if err != nil {
		return Response{}, err
	}
	return sess.execute(ctx, req)
}

func (c *ProcessClient) resolveCommand(req Request) (ServerCommand, string, error) {
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		wd, _ := os.Getwd()
		workDir = wd
	}

	filePath := strings.TrimSpace(req.FilePath)
	if filePath != "" {
		if !filepath.IsAbs(filePath) && workDir != "" {
			filePath = filepath.Join(workDir, filePath)
		}
		if cmd, ok := c.registry.Lookup(filePath); ok {
			return cmd, workDir, nil
		}
		ext := filepath.Ext(filePath)
		return ServerCommand{}, workDir, fmt.Errorf("%w: no language server registered for extension %q", ErrServerUnavailable, ext)
	}

	// workspace_symbol without a file: use first registered command.
	commands := c.registry.Commands()
	if len(commands) == 0 {
		return ServerCommand{}, workDir, fmt.Errorf("%w: no language servers configured", ErrServerUnavailable)
	}
	return commands[0], workDir, nil
}

func (c *ProcessClient) getOrStart(ctx context.Context, cmd ServerCommand, workDir string) (*session, error) {
	key := workDir + "\x00" + cmd.Language
	c.mu.Lock()
	if s, ok := c.sessions[key]; ok {
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()

	s, err := startSession(ctx, cmd, workDir)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if existing, ok := c.sessions[key]; ok {
		c.mu.Unlock()
		s.Close()
		return existing, nil
	}
	c.sessions[key] = s
	c.mu.Unlock()
	return s, nil
}

func (s *session) execute(ctx context.Context, req Request) (Response, error) {
	out := Response{Operation: req.Operation}

	switch req.Operation {
	case OperationWorkspaceSymbol:
		result, err := s.conn.Call(ctx, "workspace/symbol", map[string]any{
			"query": req.Query,
		})
		if err != nil {
			return out, err
		}
		out.Symbols = parseSymbols(result)
		return out, nil

	case OperationDocumentSymbol:
		uri, err := s.ensureOpen(ctx, resolvePath(req.WorkDir, req.FilePath))
		if err != nil {
			return out, err
		}
		result, err := s.conn.Call(ctx, "textDocument/documentSymbol", map[string]any{
			"textDocument": map[string]any{"uri": uri},
		})
		if err != nil {
			return out, err
		}
		out.Symbols = parseSymbols(result)
		return out, nil

	case OperationGoToDefinition, OperationFindReferences, OperationHover, OperationGoToImplementation:
		path := resolvePath(req.WorkDir, req.FilePath)
		uri, err := s.ensureOpen(ctx, path)
		if err != nil {
			return out, err
		}
		pos := map[string]any{
			"line":      req.Line - 1,
			"character": req.Character - 1,
		}
		tdPos := map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     pos,
		}

		var method string
		switch req.Operation {
		case OperationGoToDefinition:
			method = "textDocument/definition"
		case OperationFindReferences:
			method = "textDocument/references"
			tdPos["context"] = map[string]any{"includeDeclaration": true}
		case OperationHover:
			method = "textDocument/hover"
		case OperationGoToImplementation:
			method = "textDocument/implementation"
		}

		result, err := s.conn.Call(ctx, method, tdPos)
		if err != nil {
			return out, err
		}
		if req.Operation == OperationHover {
			out.Text = parseHover(result)
			return out, nil
		}
		out.Locations = parseLocations(result)
		return out, nil
	default:
		return out, fmt.Errorf("unsupported lsp operation: %s", req.Operation)
	}
}

func resolvePath(workDir, filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return filePath
	}
	if filepath.IsAbs(filePath) {
		return filePath
	}
	workDir = strings.TrimSpace(workDir)
	if workDir != "" {
		return filepath.Join(workDir, filePath)
	}
	return filePath
}

func parseHover(result json.RawMessage) string {
	if len(result) == 0 || string(result) == "null" {
		return ""
	}
	var hover struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(result, &hover); err != nil {
		return string(result)
	}
	return hoverContentsToString(hover.Contents)
}

func hoverContentsToString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// MarkupContent
	var mc struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &mc); err == nil && mc.Value != "" {
		return mc.Value
	}
	// MarkedString object
	var ms struct {
		Language string `json:"language"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal(raw, &ms); err == nil && ms.Value != "" {
		if ms.Language != "" {
			return "```" + ms.Language + "\n" + ms.Value + "\n```"
		}
		return ms.Value
	}
	// array of MarkedString / MarkupContent
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		parts := make([]string, 0, len(arr))
		for _, item := range arr {
			if t := hoverContentsToString(item); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return string(raw)
}

func parseLocations(result json.RawMessage) []Location {
	if len(result) == 0 || string(result) == "null" {
		return nil
	}
	// single Location
	var one wireLocation
	if err := json.Unmarshal(result, &one); err == nil && one.URI != "" {
		return []Location{one.toLocation()}
	}
	// []Location
	var many []wireLocation
	if err := json.Unmarshal(result, &many); err == nil {
		out := make([]Location, 0, len(many))
		for _, w := range many {
			if w.URI != "" {
				out = append(out, w.toLocation())
			}
		}
		return out
	}
	// LocationLink[]
	var links []struct {
		TargetURI    string `json:"targetUri"`
		TargetRange  wireRange `json:"targetRange"`
		TargetSelectionRange wireRange `json:"targetSelectionRange"`
	}
	if err := json.Unmarshal(result, &links); err == nil {
		out := make([]Location, 0, len(links))
		for _, l := range links {
			uri := l.TargetURI
			r := l.TargetSelectionRange
			if r.Start.Line == 0 && r.Start.Character == 0 && r.End.Line == 0 && r.End.Character == 0 {
				r = l.TargetRange
			}
			out = append(out, Location{
				URI:       uri,
				Line:      r.Start.Line + 1,
				Character: r.Start.Character + 1,
			})
		}
		return out
	}
	return nil
}

type wirePos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type wireRange struct {
	Start wirePos `json:"start"`
	End   wirePos `json:"end"`
}

type wireLocation struct {
	URI   string    `json:"uri"`
	Range wireRange `json:"range"`
}

func (w wireLocation) toLocation() Location {
	return Location{
		URI:       w.URI,
		Line:      w.Range.Start.Line + 1,
		Character: w.Range.Start.Character + 1,
	}
}

func parseSymbols(result json.RawMessage) []Symbol {
	if len(result) == 0 || string(result) == "null" {
		return nil
	}
	// SymbolInformation[]
	var infos []struct {
		Name     string       `json:"name"`
		Kind     int          `json:"kind"`
		Location wireLocation `json:"location"`
	}
	if err := json.Unmarshal(result, &infos); err == nil && len(infos) > 0 {
		// Heuristic: SymbolInformation has location.uri
		if infos[0].Location.URI != "" || infos[0].Name != "" {
			out := make([]Symbol, 0, len(infos))
			for _, info := range infos {
				out = append(out, Symbol{
					Name:     info.Name,
					Kind:     symbolKindName(info.Kind),
					Location: info.Location.toLocation(),
				})
			}
			// If these were actually DocumentSymbol, Location.URI empty is ok — try other parse.
			if infos[0].Location.URI != "" {
				return out
			}
		}
	}

	// DocumentSymbol[] (hierarchical)
	var docs []documentSymbol
	if err := json.Unmarshal(result, &docs); err == nil {
		var out []Symbol
		var walk func([]documentSymbol, string)
		walk = func(syms []documentSymbol, parentURI string) {
			for _, ds := range syms {
				out = append(out, Symbol{
					Name: ds.Name,
					Kind: symbolKindName(ds.Kind),
					Location: Location{
						URI:       parentURI,
						Line:      ds.SelectionRange.Start.Line + 1,
						Character: ds.SelectionRange.Start.Character + 1,
					},
				})
				if len(ds.Children) > 0 {
					walk(ds.Children, parentURI)
				}
			}
		}
		walk(docs, "")
		if len(out) > 0 {
			return out
		}
	}

	// Retry SymbolInformation more loosely
	var loose []struct {
		Name     string       `json:"name"`
		Kind     int          `json:"kind"`
		Location wireLocation `json:"location"`
	}
	if err := json.Unmarshal(result, &loose); err == nil {
		out := make([]Symbol, 0, len(loose))
		for _, info := range loose {
			out = append(out, Symbol{
				Name:     info.Name,
				Kind:     symbolKindName(info.Kind),
				Location: info.Location.toLocation(),
			})
		}
		return out
	}
	return nil
}

type documentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          wireRange        `json:"range"`
	SelectionRange wireRange        `json:"selectionRange"`
	Children       []documentSymbol `json:"children"`
}

func symbolKindName(kind int) string {
	// LSP SymbolKind enum
	names := map[int]string{
		1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class",
		6: "method", 7: "property", 8: "field", 9: "constructor", 10: "enum",
		11: "interface", 12: "function", 13: "variable", 14: "constant", 15: "string",
		16: "number", 17: "boolean", 18: "array", 19: "object", 20: "key",
		21: "null", 22: "enumMember", 23: "struct", 24: "event", 25: "operator",
		26: "typeParameter",
	}
	if n, ok := names[kind]; ok {
		return n
	}
	return fmt.Sprintf("kind_%d", kind)
}

// Ensure NoopClient still satisfies Client.
var (
	_ Client = NoopClient{}
	_ Client = (*ProcessClient)(nil)
)

// NoopClient returns a fixed "unavailable" error for every request.
type NoopClient struct{}

// Request implements Client.
func (NoopClient) Request(_ context.Context, req Request) (Response, error) {
	return Response{}, fmt.Errorf("%w for operation %s", ErrServerUnavailable, req.Operation)
}
