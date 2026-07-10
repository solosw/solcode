package codegraph

import (
	"fmt"
	"sort"
	"strings"
)

// ToDOT returns the graph in Graphviz DOT format for visualization.
// This renders all nodes and edges with colors by NodeKind and EdgeKind.
func (g *Graph) ToDOT() string {
	var b strings.Builder
	b.WriteString("digraph CodeGraph {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString(fmt.Sprintf("  label=%q;\n", g.Language))
	b.WriteString("  node [shape=box, style=filled];\n\n")

	// Collect sorted node IDs for stable output
	nodeIDs := make([]NodeID, 0, len(g.Nodes))
	for id := range g.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool {
		return string(nodeIDs[i]) < string(nodeIDs[j])
	})

	// Write nodes
	for _, id := range nodeIDs {
		node := g.Nodes[id]
		color := nodeColor(node.Kind)
		label := fmt.Sprintf("%s\\n[%s]", node.Name, node.Kind)
		if node.FullName != "" {
			label = fmt.Sprintf("%s\\n[%s]", node.FullName, node.Kind)
		}
		b.WriteString(fmt.Sprintf(
			"  %q [label=%q, fillcolor=%q, fontsize=10];\n",
			id, label, color,
		))
	}

	b.WriteString("\n")

	// Write edges
	for _, edge := range g.Edges {
		color := edgeColor(edge.Kind)
		style := edgeStyle(edge.Kind)
		b.WriteString(fmt.Sprintf(
			"  %q -> %q [color=%q, style=%q, label=%q, fontsize=8];\n",
			edge.From, edge.To, color, style, edge.Kind,
		))
	}

	b.WriteString("}\n")
	return b.String()
}

// ToMermaid returns the graph in Mermaid.js flowchart format for
// embedding in Markdown documentation.
func (g *Graph) ToMermaid() string {
	var b strings.Builder
	b.WriteString("```mermaid\ngraph LR\n")

	// Collect sorted node IDs
	nodeIDs := make([]NodeID, 0, len(g.Nodes))
	for id := range g.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool {
		return string(nodeIDs[i]) < string(nodeIDs[j])
	})

	// Build sanitized node aliases
	aliases := make(map[NodeID]string)
	for i, id := range nodeIDs {
		alias := fmt.Sprintf("N%d", i)
		aliases[NodeID(id)] = alias
		node := g.Nodes[id]
		style := mermaidShape(node.Kind)
		label := node.Name
		if node.FullName != "" {
			label = node.FullName
		}
		b.WriteString(fmt.Sprintf(
			"  %s%s[%s]%s\n",
			alias, style, label, style,
		))
	}

	// Write edges
	for _, edge := range g.Edges {
		fromAlias := aliases[edge.From]
		toAlias := aliases[edge.To]
		if fromAlias == "" || toAlias == "" {
			continue
		}
		b.WriteString(fmt.Sprintf(
			"  %s -->|%s| %s\n",
			fromAlias, edge.Kind, toAlias,
		))
	}

	b.WriteString("```\n")
	return b.String()
}

// ---------- helpers ----------

func nodeColor(kind NodeKind) string {
	switch kind {
	case KindFile:
		return "#E8F5E9"
	case KindPackage:
		return "#FFF3E0"
	case KindFunction:
		return "#E3F2FD"
	case KindMethod:
		return "#BBDEFB"
	case KindClass, KindStruct:
		return "#F3E5F5"
	case KindInterface:
		return "#E1BEE7"
	case KindEnum:
		return "#CE93D8"
	case KindTypeAlias:
		return "#D1C4E9"
	case KindVariable:
		return "#FFF9C4"
	case KindConstant:
		return "#FFECB3"
	case KindImport:
		return "#CFD8DC"
	case KindField:
		return "#FFE0B2"
	case KindParameter:
		return "#B3E5FC"
	default:
		return "#FFFFFF"
	}
}

func edgeColor(kind EdgeKind) string {
	switch kind {
	case EdgeContains:
		return "#757575"
	case EdgeCalls:
		return "#1976D2"
	case EdgeImports:
		return "#388E3C"
	case EdgeInherits:
		return "#7B1FA2"
	case EdgeDefines:
		return "#E65100"
	case EdgeReferences:
		return "#00838F"
	case EdgeImplements:
		return "#C2185B"
	case EdgeHasParam:
		return "#5D4037"
	case EdgeReturns:
		return "#455A64"
	default:
		return "#000000"
	}
}

func edgeStyle(kind EdgeKind) string {
	switch kind {
	case EdgeContains:
		return "dashed"
	case EdgeReferences:
		return "dotted"
	default:
		return "solid"
	}
}

func mermaidShape(kind NodeKind) string {
	switch kind {
	case KindFile, KindPackage:
		return "[("
	case KindInterface:
		return "{{"
	case KindFunction, KindMethod:
		return "["
	default:
		return "["
	}
}
