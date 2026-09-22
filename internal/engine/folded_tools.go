package engine

import (
	"sort"
	"strconv"
	"strings"

	"github.com/solosw/solcode/internal/tool"
)

const (
	// foldedSummaryMaxNames caps how many tool names one group lists before it
	// collapses to a count. The summary exists to tell the model what exists,
	// not to reproduce the catalog it was removed from.
	foldedSummaryMaxNames = 8
	// foldedSummaryMaxGroups caps how many groups appear, so a registry with
	// dozens of MCP servers cannot grow the prompt without bound.
	foldedSummaryMaxGroups = 12
)

// foldedToolsSummary describes the tools that are registered but were not sent
// on this turn.
//
// The registry is much larger than the wire: only core, sticky, and matched
// tools are sent as schemas. Everything else is still *executable* and still
// discoverable through ToolSearch, but the model has no way to know it exists.
// That is the gap this closes — not by shipping every schema, which is the cost
// we are avoiding, but by naming what is available so the model knows when to
// search.
//
// Naming matters: a model told "ToolSearch can find more" will not search,
// because it has no reason to suspect anything relevant is missing. A model told
// "browser_click, browser_type are available via ToolSearch" knows.
func foldedToolsSummary(all []tool.Tool, sent []tool.Tool) string {
	if len(all) == 0 {
		return ""
	}
	sentNames := make(map[string]bool, len(sent))
	for _, candidate := range sent {
		if candidate == nil {
			continue
		}
		sentNames[candidate.Name()] = true
	}

	type group struct {
		names []string
	}
	groups := make(map[string]*group)
	for _, candidate := range all {
		if candidate == nil {
			continue
		}
		name := candidate.Name()
		if sentNames[name] || hiddenFromModel[name] {
			continue
		}
		key := foldedGroupKey(name)
		if groups[key] == nil {
			groups[key] = &group{}
		}
		groups[key].names = append(groups[key].names, name)
	}
	if len(groups) == 0 {
		return ""
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > foldedSummaryMaxGroups {
		keys = keys[:foldedSummaryMaxGroups]
	}

	var b strings.Builder
	b.WriteString("Additional capabilities available via ToolSearch (not loaded this turn):\n")
	total := 0
	for _, key := range keys {
		names := groups[key].names
		sort.Strings(names)
		total += len(names)
		list := names
		truncated := 0
		if len(list) > foldedSummaryMaxNames {
			truncated = len(list) - foldedSummaryMaxNames
			list = list[:foldedSummaryMaxNames]
		}
		line := "- " + key + ": " + strings.Join(list, ", ")
		if truncated > 0 {
			line += " (+" + strconv.Itoa(truncated) + " more)"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("Call ToolSearch with a capability query to enable any of these. Do not assume a capability is missing just because it is not listed above.\n")
	return strings.TrimSpace(b.String())
}

// foldedGroupKey groups hidden tools for a compact listing: MCP tools by server,
// everything else under its own name.
func foldedGroupKey(name string) string {
	if strings.HasPrefix(name, tool.MCPToolPrefix) {
		rest := strings.TrimPrefix(name, tool.MCPToolPrefix)
		if idx := strings.Index(rest, "__"); idx > 0 {
			return "MCP " + rest[:idx]
		}
		return "MCP"
	}
	return "built-in"
}
