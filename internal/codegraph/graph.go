// Package codegraph builds code knowledge graphs from source files using
// tree-sitter for multi-language syntax parsing.
//
// The graph captures symbols (functions, classes, variables, imports) as nodes
// and their relationships (calls, contains, imports, inherits) as edges,
// enabling code navigation, analysis, and visualization.
package codegraph

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// NodeKind classifies a code entity.
type NodeKind string

const (
	KindPackage   NodeKind = "package"
	KindFile      NodeKind = "file"
	KindFunction  NodeKind = "function"
	KindMethod    NodeKind = "method"
	KindClass     NodeKind = "class"
	KindStruct    NodeKind = "struct"
	KindInterface NodeKind = "interface"
	KindVariable  NodeKind = "variable"
	KindConstant  NodeKind = "constant"
	KindImport    NodeKind = "import"
	KindModule    NodeKind = "module"
	KindEnum      NodeKind = "enum"
	KindField     NodeKind = "field"
	KindParameter NodeKind = "parameter"
	KindTypeAlias NodeKind = "type_alias"
)

// EdgeKind classifies a relationship between two code entities.
type EdgeKind string

const (
	EdgeContains   EdgeKind = "contains"
	EdgeCalls      EdgeKind = "calls"
	EdgeImports    EdgeKind = "imports"
	EdgeInherits   EdgeKind = "inherits"
	EdgeDefines    EdgeKind = "defines"    // type defines method
	EdgeReferences EdgeKind = "references" // variable/type reference
	EdgeImplements EdgeKind = "implements"
	EdgeHasParam   EdgeKind = "has_param"
	EdgeReturns    EdgeKind = "returns"
)

// Node represents a code entity in the graph.
type Node struct {
	ID       NodeID            `json:"id"`
	Kind     NodeKind          `json:"kind"`
	Name     string            `json:"name"`
	FullName string            `json:"full_name,omitempty"`
	File     string            `json:"file"`
	Line     int               `json:"line"`
	Column   int               `json:"column"`
	EndLine  int               `json:"end_line,omitempty"`
	EndCol   int               `json:"end_col,omitempty"`
	Language string            `json:"language"`
	Extra    map[string]string `json:"extra,omitempty"`
}

// NodeID is a stable unique identifier for a node within a graph.
type NodeID string

// Edge represents a directed relationship between two nodes.
type Edge struct {
	From NodeID   `json:"from"`
	To   NodeID   `json:"to"`
	Kind EdgeKind `json:"kind"`
	Line int      `json:"line,omitempty"`
	Col  int      `json:"col,omitempty"`
}

// Graph is a code knowledge graph for a set of source files.
type Graph struct {
	Language string           `json:"language"`
	Nodes    map[NodeID]*Node `json:"nodes"`
	Edges    []*Edge          `json:"edges"`
}

// NewGraph creates an empty graph for the given language.
func NewGraph(language string) *Graph {
	return &Graph{
		Language: language,
		Nodes:    make(map[NodeID]*Node),
		Edges:    make([]*Edge, 0),
	}
}

// AddNode adds a node to the graph, generating a stable ID if empty.
func (g *Graph) AddNode(node *Node) NodeID {
	if node.ID == "" {
		node.ID = g.generateID(node)
	}
	if existing, ok := g.Nodes[node.ID]; ok {
		// Merge extra fields from duplicate
		for k, v := range node.Extra {
			if existing.Extra == nil {
				existing.Extra = make(map[string]string)
			}
			existing.Extra[k] = v
		}
		return node.ID
	}
	g.Nodes[node.ID] = node
	return node.ID
}

// AddEdge adds a directed edge to the graph.
func (g *Graph) AddEdge(edge *Edge) {
	g.Edges = append(g.Edges, edge)
}

// GetNode returns a node by ID, or nil if not found.
func (g *Graph) GetNode(id NodeID) *Node {
	return g.Nodes[id]
}

// FindNodesByKind returns all nodes of a given kind.
func (g *Graph) FindNodesByKind(kind NodeKind) []*Node {
	var result []*Node
	for _, n := range g.Nodes {
		if n.Kind == kind {
			result = append(result, n)
		}
	}
	return result
}

// FindNodeByName finds the first node matching kind and name.
func (g *Graph) FindNodeByName(kind NodeKind, name string) *Node {
	for _, n := range g.Nodes {
		if n.Kind == kind && n.Name == name {
			return n
		}
	}
	return nil
}

// FindNodesByFile returns all nodes defined in a given file.
func (g *Graph) FindNodesByFile(file string) []*Node {
	var result []*Node
	for _, n := range g.Nodes {
		if n.File == file {
			result = append(result, n)
		}
	}
	return result
}

// Stats returns summary statistics for the graph.
func (g *Graph) Stats() GraphStats {
	stats := GraphStats{
		TotalNodes: len(g.Nodes),
		TotalEdges: len(g.Edges),
		ByKind:     make(map[NodeKind]int),
		ByEdgeKind: make(map[EdgeKind]int),
	}
	for _, n := range g.Nodes {
		stats.ByKind[n.Kind]++
	}
	for _, e := range g.Edges {
		stats.ByEdgeKind[e.Kind]++
	}
	return stats
}

// GraphStats holds summary counts for a graph.
type GraphStats struct {
	TotalNodes int               `json:"total_nodes"`
	TotalEdges int               `json:"total_edges"`
	ByKind     map[NodeKind]int  `json:"by_kind"`
	ByEdgeKind map[EdgeKind]int  `json:"by_edge_kind"`
}

// String returns a human-readable summary.
func (s GraphStats) String() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Nodes: %d\n", s.TotalNodes))
	for k, v := range s.ByKind {
		b.WriteString(fmt.Sprintf("  %s: %d\n", k, v))
	}
	b.WriteString(fmt.Sprintf("Edges: %d\n", s.TotalEdges))
	for k, v := range s.ByEdgeKind {
		b.WriteString(fmt.Sprintf("  %s: %d\n", k, v))
	}
	return b.String()
}

// generateID creates a stable unique node ID from its properties.
func (g *Graph) generateID(node *Node) NodeID {
	file := filepath.Base(node.File)
	return NodeID(fmt.Sprintf("%s/%s/%s", file, node.Kind, node.Name))
}

// ToJSON serializes the graph as indented JSON.
func (g *Graph) ToJSON() ([]byte, error) {
	return json.MarshalIndent(g, "", "  ")
}

// MergeGraph merges another graph into this one, re-indexing node IDs
// to avoid collisions by prefixing with the source graph's language.
func (g *Graph) MergeGraph(other *Graph) {
	for id, node := range other.Nodes {
		// Re-key with a prefix to avoid collisions
		newID := NodeID(fmt.Sprintf("%s::%s", other.Language, id))
		node.ID = newID
		g.Nodes[newID] = node
	}
	for _, edge := range other.Edges {
		fromID := NodeID(fmt.Sprintf("%s::%s", other.Language, edge.From))
		toID := NodeID(fmt.Sprintf("%s::%s", other.Language, edge.To))
		g.AddEdge(&Edge{
			From: fromID,
			To:   toID,
			Kind: edge.Kind,
			Line: edge.Line,
			Col:  edge.Col,
		})
	}
}
