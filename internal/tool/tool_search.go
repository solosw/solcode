package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/solosw/solcode/internal/skill"
)

const ToolSearchToolName = "ToolSearch"

type toolSearchTool struct {
	BaseTool
	registry *Registry
	skills   *skill.Registry
}

// NewToolSearchTool creates a lightweight discovery tool over the current
// registry. It deliberately searches at invocation time so MCP and skill
// changes are reflected without a static catalog.
func NewToolSearchTool(registry *Registry, skills *skill.Registry) Tool {
	return &toolSearchTool{registry: registry, skills: skills}
}

func (t *toolSearchTool) Name() string { return ToolSearchToolName }

func (t *toolSearchTool) Description() string {
	return "Search the currently available built-in tools, MCP tools, and skills by capability. The current tool list is incomplete: MCP tools and less common builtins are hidden until discovered. Call this as soon as a needed capability is not listed (web, fetch, images, browser, extra MCP servers, named skills). Matching tools are enabled on the following turn; then call the exact returned name. Do not invent tool names."
}

func (t *toolSearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Capability or task to search for.",
			},
			"kind": map[string]any{
				"type":        "string",
				"enum":        []string{"all", "tool", "skill"},
				"description": "Optional result type. Defaults to all.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum matches to return, from 1 to 10. Defaults to 5.",
			},
		},
		"required": []string{"query"},
	}
}

func (t *toolSearchTool) IsReadOnly(json.RawMessage) bool { return true }

func (t *toolSearchTool) Invoke(_ context.Context, _ *UseContext, input json.RawMessage) (*ContentBlock, error) {
	matches, err := SearchCapabilities(t.registry, t.skills, input)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	if len(matches) == 0 {
		return Result("No matching capability is currently available. Try different terms or check configured MCP servers and skill directories."), nil
	}

	var b strings.Builder
	b.WriteString("Matches (they will be considered on the next turn):\n")
	for _, item := range matches {
		desc := strings.TrimSpace(item.Description)
		if len(desc) > 180 {
			desc = desc[:177] + "..."
		}
		fmt.Fprintf(&b, "- %s %s: %s\n", item.Kind, item.Name, desc)
	}
	b.WriteString("Use an exact tool name only after it appears in the available tools; activate a skill with the Skill tool.")
	return Result(strings.TrimSpace(b.String())), nil
}

// CapabilityMatch is one live registry/skill hit for ToolSearch or routing.
type CapabilityMatch struct {
	Kind        string // "tool" or "skill"
	Name        string
	Description string
	Score       int
}

// SearchCapabilities scores the live tool and skill registries against the
// ToolSearch input payload. The engine uses the returned tool names as the
// sticky enable set for subsequent turns.
func SearchCapabilities(registry *Registry, skills *skill.Registry, input json.RawMessage) ([]CapabilityMatch, error) {
	var params struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return nil, fmt.Errorf("invalid ToolSearch input: %w", err)
	}
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	kind := strings.ToLower(strings.TrimSpace(params.Kind))
	if kind == "" {
		kind = "all"
	}
	if kind != "all" && kind != "tool" && kind != "skill" {
		return nil, fmt.Errorf("kind must be all, tool, or skill")
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}

	matches := make([]CapabilityMatch, 0)
	if kind == "all" || kind == "tool" {
		if registry != nil {
			for _, candidate := range registry.All() {
				if candidate.Name() == ToolSearchToolName {
					continue
				}
				if candidate.Name() == WaitToolName || candidate.Name() == SubagentToolName {
					// Wait/Subagent are internal helpers, not model-facing tools.
					continue
				}
				score := capabilityScore(query, candidate.Name(), candidate.Description())
				if score > 0 {
					matches = append(matches, CapabilityMatch{
						Kind:        "tool",
						Name:        candidate.Name(),
						Description: candidate.Description(),
						Score:       score,
					})
				}
			}
		}
	}
	if kind == "all" || kind == "skill" {
		if skills != nil {
			for _, candidate := range skills.All() {
				score := capabilityScore(query, candidate.Name, candidate.Description)
				if score > 0 {
					matches = append(matches, CapabilityMatch{
						Kind:        "skill",
						Name:        candidate.Name,
						Description: candidate.Description,
						Score:       score,
					})
				}
			}
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].Kind != matches[j].Kind {
			return matches[i].Kind < matches[j].Kind
		}
		return matches[i].Name < matches[j].Name
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// CapabilityScore is shared by the runtime selector and ToolSearch. It is
// intentionally lexical: capability data comes from live MCP/skill metadata,
// so no provider-specific domain map is needed.
func CapabilityScore(query, name, description string) int {
	return capabilityScore(query, name, description)
}

func capabilityScore(query, name, description string) int {
	terms := capabilityTerms(query)
	if len(terms) == 0 {
		return 0
	}
	name = strings.ToLower(name)
	description = strings.ToLower(description)
	score := 0
	for _, term := range terms {
		if strings.Contains(name, term) {
			score += 4
		}
		if strings.Contains(description, term) {
			score++
		}
	}
	return score
}

// CapabilityTerms tokenizes free-form text for lexical capability matching.
func CapabilityTerms(text string) []string {
	return capabilityTerms(text)
}

func capabilityTerms(text string) []string {
	seen := make(map[string]bool)
	terms := make([]string, 0)
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if len([]rune(token)) < 2 || seen[token] {
			continue
		}
		seen[token] = true
		terms = append(terms, token)
	}
	return terms
}
