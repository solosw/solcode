package engine

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/solosw/solcode/internal/tool"
)

const maxDynamicTools = 4

// coreToolNames are always exposed when dynamic routing is active. They cover
// the agent loop and common coding edits. MCP tools and less common builtins
// stay off the wire until lexical match, sticky use, or ToolSearch enables them.
var coreToolNames = map[string]bool{
	tool.AskUserToolName:         true,
	tool.BashToolName:            true,
	tool.EditToolName:            true,
	tool.MultiEditToolName:       true,
	tool.MultiWriteToolName:      true,
	tool.GlobToolName:            true,
	tool.GrepToolName:            true,
	tool.LSToolName:              true,
	tool.ModeSwitchToolName:      true,
	"LSP":                        true,
	tool.TaskToolName:            true,
	tool.TodoWriteToolName:       true,
	tool.ToolSearchToolName:      true,
	tool.SkillToolName:           true,
	tool.ViewToolName:            true,
	tool.WriteToolName:           true,
	tool.WriteMemoryToolName:     true,
	tool.ReadMemoryToolName:      true,
	tool.ReadObservationToolName: true,
	// Session memory is written once at session end and searched later, so it
	// cannot rely on lexical matching against the current turn's prompt.
	tool.WriteSessionMemoryToolName: true,
	tool.ReadSessionMemoryToolName:  true,
}

// hiddenFromModel tools stay registered for execution/tests but are never
// exposed on the model tool list (including ToolSearch sticky enable).
var hiddenFromModel = map[string]bool{
	tool.WaitToolName:     true, // Bash timeout > 3m auto-waits; Wait is internal
	tool.SubagentToolName: true, // Task orchestrates Subagent; not model-visible
}

// SelectToolsForTurn keeps the full registry available for execution and
// discovery, but returns only a compact core plus live matches for the model
// request. Matching uses current tool metadata only, so dynamically connected
// MCP servers do not require a hard-coded profile or provider map.
//
// allowed semantics match Registry.Filter for non-empty whitelists: a non-empty
// allowed list is treated as an explicit restriction (for example Task
// sub-agents). nil or empty allowed enables dynamic routing over all tools.
// Wait and Subagent are never model-visible (see hiddenFromModel).
func SelectToolsForTurn(all []tool.Tool, allowed []string, query string, enabled map[string]bool) []tool.Tool {
	selected, _ := selectToolsForTurn(all, allowed, query, enabled)
	return selected
}

// selectToolsForTurn is SelectToolsForTurn with a report of which tools the
// current selection contains. The report lets the semantic router ask only
// about candidates lexical matching could not resolve.
func selectToolsForTurn(all []tool.Tool, allowed []string, query string, enabled map[string]bool) ([]tool.Tool, map[string]bool) {
	if len(allowed) > 0 {
		selected := filterTools(all, allowed)
		names := make(map[string]bool, len(selected))
		for _, candidate := range selected {
			names[candidate.Name()] = true
		}
		return selected, names
	}
	selected := make(map[string]bool)
	for _, candidate := range all {
		name := candidate.Name()
		if hiddenFromModel[name] {
			continue
		}
		if coreToolNames[name] {
			selected[name] = true
		}
	}
	for name := range enabled {
		name = strings.TrimSpace(name)
		if name == "" || hiddenFromModel[name] {
			continue
		}
		selected[name] = true
	}

	type scoredTool struct {
		tool  tool.Tool
		score int
	}
	matches := make([]scoredTool, 0)
	for _, candidate := range all {
		name := candidate.Name()
		if selected[name] || hiddenFromModel[name] {
			continue
		}
		score := tool.CapabilityScore(query, name, candidate.Description())
		if score > 0 {
			matches = append(matches, scoredTool{tool: candidate, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].tool.Name() < matches[j].tool.Name()
	})
	for i := 0; i < len(matches) && i < maxDynamicTools; i++ {
		selected[matches[i].tool.Name()] = true
	}

	out := make([]tool.Tool, 0, len(selected))
	for _, candidate := range all {
		if selected[candidate.Name()] {
			out = append(out, candidate)
		}
	}
	return out, selected
}

func filterTools(all []tool.Tool, allowed []string) []tool.Tool {
	allowedNames := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		name = strings.TrimSpace(name)
		if name == "" || hiddenFromModel[name] {
			continue
		}
		allowedNames[name] = true
	}
	if len(allowedNames) == 0 {
		// Unrestricted empty allowed is handled by the caller; here it means
		// the whitelist only contained hidden names.
		return nil
	}
	out := make([]tool.Tool, 0, len(allowedNames))
	for _, candidate := range all {
		if allowedNames[candidate.Name()] {
			out = append(out, candidate)
		}
	}
	return out
}

func toolSearchQuery(input []byte) string {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ""
	}
	return strings.TrimSpace(params.Query)
}

// enableToolsFromSearch runs the same live registry search as ToolSearch and
// marks matching tool names sticky for later turns.
func enableToolsFromSearch(all []tool.Tool, input []byte, enabled map[string]bool) {
	if enabled == nil {
		return
	}
	// Build a temporary registry view from the live tool slice so search stays
	// in sync with whatever the engine currently holds.
	reg := tool.NewRegistry()
	reg.Register(all...)
	matches, err := tool.SearchCapabilities(reg, nil, input)
	if err != nil {
		// Fall back to query-only lexical selection against the current tools.
		query := toolSearchQuery(input)
		if query == "" {
			return
		}
		for _, candidate := range SelectToolsForTurn(all, nil, query, nil) {
			if coreToolNames[candidate.Name()] {
				continue
			}
			enabled[candidate.Name()] = true
		}
		return
	}
	for _, match := range matches {
		if match.Kind != "tool" {
			continue
		}
		if hiddenFromModel[match.Name] {
			continue
		}
		enabled[match.Name] = true
	}
}

// enableToolsFromQuery is the engine-side equivalent of the model calling
// ToolSearch with kind=tool. Used when Jev routing runs on a lexical miss but
// enables nothing: only tools that actually match the query become sticky.
// No MCP server is force-enabled just because it is registered. A total miss
// leaves FoldedTools / the model's own ToolSearch as the discovery path.
func enableToolsFromQuery(all []tool.Tool, query string, enabled map[string]bool) {
	query = strings.TrimSpace(query)
	if query == "" || enabled == nil {
		return
	}
	input, err := json.Marshal(map[string]any{
		"query": query,
		"kind":  "tool",
		"limit": 5,
	})
	if err != nil {
		return
	}
	enableToolsFromSearch(all, input, enabled)
}

// hasNonCoreEnabled reports whether sticky enablement already includes a
// non-core, model-visible tool. Used to decide whether a ToolSearch fallback
// is still needed after Jev routing.
func hasNonCoreEnabled(enabled map[string]bool) bool {
	for name := range enabled {
		name = strings.TrimSpace(name)
		if name == "" || coreToolNames[name] || hiddenFromModel[name] {
			continue
		}
		return true
	}
	return false
}

// selectionQuery combines the user prompt with recent message text so later
// turns can still surface relevant MCP tools without re-sending the full catalog.
func selectionQuery(prompt string, messagesText string) string {
	prompt = strings.TrimSpace(prompt)
	messagesText = strings.TrimSpace(messagesText)
	switch {
	case prompt == "":
		return messagesText
	case messagesText == "":
		return prompt
	default:
		return prompt + "\n" + messagesText
	}
}
