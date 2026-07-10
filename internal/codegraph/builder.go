package codegraph

import (
	"fmt"
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// GraphBuilder constructs code knowledge graphs from source files using
// tree-sitter for parsing and language-specific queries for symbol extraction.
type GraphBuilder struct {
	// FailOnParseError causes Build to return an error when parsing fails.
	FailOnParseError bool
}

// NewGraphBuilder creates a new GraphBuilder with default settings.
func NewGraphBuilder() *GraphBuilder {
	return &GraphBuilder{}
}

// Build parses source code and extracts a code knowledge graph.
// It auto-detects the language from filename extension.
func (b *GraphBuilder) Build(src []byte, filename string) (*Graph, error) {
	cfg, ok := DetectFromFile(filename)
	if !ok {
		return nil, fmt.Errorf("codegraph: unsupported file extension for %q", filename)
	}
	return b.BuildWithConfig(src, filename, cfg)
}

// BuildWithConfig parses source code using an explicit LanguageConfig.
func (b *GraphBuilder) BuildWithConfig(src []byte, filename string, cfg *LanguageConfig) (*Graph, error) {
	if cfg.LanguageFunc == nil {
		return nil, fmt.Errorf("codegraph: LanguageConfig for %q has nil LanguageFunc", cfg.Name)
	}

	lang := cfg.LanguageFunc()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		if b.FailOnParseError {
			return nil, fmt.Errorf("codegraph: parse error for %q: %w", filename, err)
		}
		// Return partial graph with generic walk on error
		graph := NewGraph(cfg.Name)
		b.addFileNode(graph, filename, len(src))
		return graph, nil
	}
	defer tree.Release()

	graph := NewGraph(cfg.Name)

	// Add file-level node
	b.addFileNode(graph, filename, len(src))

	// Extract symbols using language-specific queries
	b.extractSymbols(graph, tree.RootNode(), lang, src, filename, cfg)

	// Extract call edges
	b.extractCalls(graph, tree.RootNode(), lang, src, filename, cfg)

	// Extract import edges
	b.extractImports(graph, tree.RootNode(), lang, src, filename, cfg)

	// Create containment edges (file → top-level declarations)
	b.addContainmentEdges(graph, filename)

	// If no language-specific queries configured, do generic traversal
	if isEmptyQuerySet(cfg.Queries) {
		b.genericWalk(graph, tree.RootNode(), lang, src, filename)
	}

	return graph, nil
}

// addFileNode adds a file-level node to the graph.
func (b *GraphBuilder) addFileNode(g *Graph, filename string, srcLen int) {
	g.AddNode(&Node{
		ID:       NodeID(filename),
		Kind:     KindFile,
		Name:     filename,
		File:     filename,
		Line:     0,
		Column:   0,
		Language: g.Language,
	})
}

// extractSymbols runs all declaration queries against the tree.
func (b *GraphBuilder) extractSymbols(g *Graph, root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, filename string, cfg *LanguageConfig) {
	q := cfg.Queries

	b.runQuery(g, root, lang, src, filename, q.Package, KindPackage, noopEnrich)
	b.runQuery(g, root, lang, src, filename, q.Function, KindFunction, func(n *Node, m *nodeWithMatch) {
		if n.Kind == KindFunction {
			// Functions at top level; methods have a receiver
			recvType := b.captureText(m, "recv_type", src)
			if recvType != "" {
				n.Kind = KindMethod
				n.FullName = recvType + "." + n.Name
				n.Extra["receiver"] = recvType
			}
		}
	})
	b.runQuery(g, root, lang, src, filename, q.Method, KindMethod, nil)
	b.runQuery(g, root, lang, src, filename, q.Class, KindClass, nil)
	b.runQuery(g, root, lang, src, filename, q.Struct, KindStruct, nil)
	b.runQuery(g, root, lang, src, filename, q.Interface, KindInterface, nil)
	b.runQuery(g, root, lang, src, filename, q.Enum, KindEnum, nil)
	b.runQuery(g, root, lang, src, filename, q.TypeAlias, KindTypeAlias, nil)
	b.runQuery(g, root, lang, src, filename, q.Variable, KindVariable, nil)
	b.runQuery(g, root, lang, src, filename, q.Constant, KindConstant, nil)
	b.runQuery(g, root, lang, src, filename, q.Field, KindField, nil)
}

// extractCalls runs the call query and adds EdgeCalls edges.
func (b *GraphBuilder) extractCalls(g *Graph, root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, filename string, cfg *LanguageConfig) {
	if cfg.Queries.Call == "" {
		return
	}
	query, err := gotreesitter.NewQuery(cfg.Queries.Call, lang)
	if err != nil {
		return
	}

	cursor := query.Exec(root, lang, src)
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		var calleeName string
		var callLine, callCol int
		for _, cap := range match.Captures {
			if cap.Name == "callee" {
				calleeName = cap.Node.Text(src)
				callLine = int(cap.Node.StartPoint().Row)
				callCol = int(cap.Node.StartPoint().Column)
			}
		}
		if calleeName == "" {
			continue
		}
		// Find the enclosing function
		caller := b.findEnclosingFunction(g, root, lang, src, callLine)
		if caller == nil {
			continue
		}
		// Find or create the callee node
		callee := g.FindNodeByName(KindFunction, calleeName)
		if callee == nil {
			callee = g.FindNodeByName(KindMethod, calleeName)
		}
		if callee == nil {
			// External or unresolved — still record as an edge to a placeholder
			calleeID := NodeID(fmt.Sprintf("%s/%s/%s", filename, KindFunction, calleeName))
			if _, exists := g.Nodes[calleeID]; !exists {
				g.AddNode(&Node{
					ID:       calleeID,
					Kind:     KindFunction,
					Name:     calleeName,
					File:     filename,
					Line:     callLine,
					Column:   callCol,
					Language: g.Language,
				})
			}
			callee = g.Nodes[calleeID]
		}
		g.AddEdge(&Edge{
			From: caller.ID,
			To:   callee.ID,
			Kind: EdgeCalls,
			Line: callLine,
			Col:  callCol,
		})
	}
}

// extractImports runs the import query and adds EdgeImports edges.
func (b *GraphBuilder) extractImports(g *Graph, root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, filename string, cfg *LanguageConfig) {
	if cfg.Queries.Import == "" {
		return
	}
	query, err := gotreesitter.NewQuery(cfg.Queries.Import, lang)
	if err != nil {
		return
	}

	cursor := query.Exec(root, lang, src)
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		var importPath string
		var importLine, importCol int
		for _, cap := range match.Captures {
			switch cap.Name {
			case "path", "name":
				text := cap.Node.Text(src)
				// Strip quotes for Go imports
				text = strings.Trim(text, `"`)
				text = strings.Trim(text, `'`)
				importPath = text
				importLine = int(cap.Node.StartPoint().Row)
				importCol = int(cap.Node.StartPoint().Column)
			}
		}
		if importPath == "" {
			continue
		}

		importID := NodeID(fmt.Sprintf("%s/%s/%s", filename, KindImport, importPath))
		g.AddNode(&Node{
			ID:       importID,
			Kind:     KindImport,
			Name:     importPath,
			File:     filename,
			Line:     importLine,
			Column:   importCol,
			Language: g.Language,
		})

		fileNode := g.GetNode(NodeID(filename))
		if fileNode != nil {
			g.AddEdge(&Edge{
				From: fileNode.ID,
				To:   importID,
				Kind: EdgeImports,
				Line: importLine,
				Col:  importCol,
			})
		}
	}
}

// addContainmentEdges creates EdgeContains from file → top-level declarations.
func (b *GraphBuilder) addContainmentEdges(g *Graph, filename string) {
	fileNode := g.GetNode(NodeID(filename))
	if fileNode == nil {
		return
	}
	topLevelKinds := []NodeKind{
		KindPackage, KindFunction, KindMethod, KindClass, KindStruct,
		KindInterface, KindEnum, KindTypeAlias, KindVariable, KindConstant,
	}
	for _, node := range g.Nodes {
		if node.File != filename || node.ID == fileNode.ID {
			continue
		}
		for _, kind := range topLevelKinds {
			if node.Kind == kind {
				g.AddEdge(&Edge{
					From: fileNode.ID,
					To:   node.ID,
					Kind: EdgeContains,
				})
				break
			}
		}
	}
}

// genericWalk performs a generic AST traversal for languages without
// specific query configurations. It creates nodes for all named nodes
// and basic containment edges.
func (b *GraphBuilder) genericWalk(g *Graph, root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, filename string) {
	b.walkNode(g, root, lang, src, filename, "")
}

// walkNode recursively walks a node and its children.
func (b *GraphBuilder) walkNode(g *Graph, node *gotreesitter.Node, lang *gotreesitter.Language, src []byte, filename string, parentID NodeID) {
	if node == nil {
		return
	}

	kind := mapNodeTypeToKind(node.Type(lang))
	name := node.Text(src)

	// Only create nodes for named nodes with meaningful content
	if node.IsNamed() && kind != "" && name != "" {
		n := &Node{
			Kind:     kind,
			Name:     name,
			File:     filename,
			Line:     int(node.StartPoint().Row),
			Column:   int(node.StartPoint().Column),
			EndLine:  int(node.EndPoint().Row),
			EndCol:   int(node.EndPoint().Column),
			Language: g.Language,
		}
		nid := g.AddNode(n)

		if parentID != "" {
			g.AddEdge(&Edge{
				From: parentID,
				To:   nid,
				Kind: EdgeContains,
			})
		}
		parentID = nid
	}

	// Walk children
	for i := 0; i < node.ChildCount(); i++ {
		child := node.Child(i)
		b.walkNode(g, child, lang, src, filename, parentID)
	}
}

// runQuery executes a tree-sitter query and adds nodes to the graph.
func (b *GraphBuilder) runQuery(g *Graph, root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, filename string, pattern string, kind NodeKind, enrich enrichFunc) {
	if pattern == "" {
		return
	}

	query, err := gotreesitter.NewQuery(pattern, lang)
	if err != nil {
		return
	}

	cursor := query.Exec(root, lang, src)
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		var nodeName string
		var startLine, endLine, endCol int
		captures := make(map[string]string)

		for _, cap := range match.Captures {
			text := cap.Node.Text(src)
			captures[cap.Name] = text

			switch cap.Name {
			case "name":
				nodeName = text
				startLine = int(cap.Node.StartPoint().Row)
			case "func", "method", "class", "struct", "interface",
				"var", "const", "import", "pkg", "enum", "alias", "field":
				endLine = int(cap.Node.EndPoint().Row)
				endCol = int(cap.Node.EndPoint().Column)
				// Set full text as the node text content
			}
		}

		if nodeName == "" {
			continue
		}

		n := &Node{
			Kind:     kind,
			Name:     nodeName,
			File:     filename,
			Line:     startLine + 1, // 1-based for display
			EndLine:  endLine + 1,
			EndCol:   endCol,
			Language: g.Language,
			Extra:    make(map[string]string),
		}

		// Store captured data in Extra
		for k, v := range captures {
			if k != "name" {
				n.Extra[k] = v
			}
		}

		// Apply enrichment
		if enrich != nil {
			m := &nodeWithMatch{node: n, captures: captures, src: src}
			enrich(n, m)
		}

		g.AddNode(n)
	}
}

// findEnclosingFunction finds the function/method that contains the given line.
func (b *GraphBuilder) findEnclosingFunction(g *Graph, root *gotreesitter.Node, lang *gotreesitter.Language, src []byte, line int) *Node {
	var best *Node
	for _, n := range g.Nodes {
		if (n.Kind == KindFunction || n.Kind == KindMethod) &&
			n.Line-1 <= line && n.EndLine >= line {
			if best == nil || n.Line > best.Line {
				best = n
			}
		}
	}
	return best
}

// captureText returns text for a named capture from a match.
func (b *GraphBuilder) captureText(m *nodeWithMatch, name string, src []byte) string {
	if m == nil {
		return ""
	}
	if s, ok := m.captures[name]; ok {
		return s
	}
	return ""
}

// ---------- helper types ----------

type enrichFunc func(n *Node, m *nodeWithMatch)

type nodeWithMatch struct {
	node     *Node
	captures map[string]string
	src      []byte
}

// mapNodeTypeToKind maps common tree-sitter node types to NodeKind.
func mapNodeTypeToKind(tsType string) NodeKind {
	switch {
	case strings.Contains(tsType, "function"):
		return KindFunction
	case strings.Contains(tsType, "method"):
		return KindMethod
	case strings.Contains(tsType, "class"):
		return KindClass
	case strings.Contains(tsType, "struct"):
		return KindStruct
	case strings.Contains(tsType, "interface"):
		return KindInterface
	case strings.Contains(tsType, "enum"):
		return KindEnum
	case strings.Contains(tsType, "variable"), strings.Contains(tsType, "assignment"):
		return KindVariable
	case strings.Contains(tsType, "import"), strings.Contains(tsType, "include"):
		return KindImport
	case strings.Contains(tsType, "package"), strings.Contains(tsType, "module"):
		return KindPackage
	case strings.Contains(tsType, "field"), strings.Contains(tsType, "property"):
		return KindField
	case strings.Contains(tsType, "parameter"):
		return KindParameter
	case strings.Contains(tsType, "type_alias"):
		return KindTypeAlias
	default:
		return ""
	}
}

// noopEnrich is a no-op enrichment function.
func noopEnrich(n *Node, m *nodeWithMatch) {}

// isEmptyQuerySet returns true if all query fields are empty.
func isEmptyQuerySet(q QuerySet) bool {
	return q.Function == "" && q.Method == "" && q.Class == "" &&
		q.Struct == "" && q.Interface == "" && q.Enum == "" &&
		q.TypeAlias == "" && q.Variable == "" && q.Constant == "" &&
		q.Import == "" && q.Package == "" && q.Field == "" &&
		q.Call == "" && q.Inherits == "" && q.Implements == ""
}
