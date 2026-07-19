package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/skill"
)

const SkillToolName = "Skill"

type SkillParams struct {
	Skill string `json:"skill"`
	Args  string `json:"args,omitempty"`
}

type skillTool struct {
	BaseTool
	registry *skill.Registry
}

func NewSkillTool(registry *skill.Registry) Tool {
	return &skillTool{registry: registry}
}

func (t *skillTool) Name() string { return SkillToolName }
func (t *skillTool) Description() string {
	return `Execute a configured skill within the conversation.
Use this when a task matches a reusable project or user workflow (Agent Skills package).
After activation, follow the instructions. Bundled scripts/references/assets live under
the skill Root (which may be outside the project WorkDir). Prefer the absolute paths
listed in the tool result, or skill-relative paths like references/foo.md (tools resolve
those against skill roots).`
}
func (t *skillTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skill": map[string]any{
				"type":        "string",
				"description": "The skill name to invoke",
			},
			"args": map[string]any{
				"type":        "string",
				"description": "Optional free-form arguments passed to the skill",
			},
		},
		"required": []string{"skill"},
	}
}
func (t *skillTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *skillTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *skillTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *skillTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	_ = ctx
	_ = uctx
	if t.registry == nil {
		return ErrorResult("skill registry is not configured"), nil
	}
	var params SkillParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	if strings.TrimSpace(params.Skill) == "" {
		return ErrorResult("skill is required"), nil
	}
	def, ok := t.registry.Find(params.Skill)
	if !ok {
		return ErrorResult("unknown skill: " + params.Skill), nil
	}
	body, err := def.ReadInstructions()
	if err != nil {
		return ErrorResult("read skill: " + err.Error()), nil
	}
	if strings.TrimSpace(body) == "" {
		body = fmt.Sprintf("Skill %s has empty instructions.", def.Name)
	}

	root := def.Root()
	var b strings.Builder
	fmt.Fprintf(&b, "[Skill: %s]\n", def.Name)
	if params.Args != "" {
		fmt.Fprintf(&b, "Args: %s\n", params.Args)
	}
	if desc := strings.TrimSpace(def.Description); desc != "" {
		fmt.Fprintf(&b, "Description: %s\n", desc)
	}
	if root != "" {
		fmt.Fprintf(&b, "Root: %s\n", root)
	}
	if tools := strings.TrimSpace(def.AllowedTools); tools != "" {
		fmt.Fprintf(&b, "Allowed-tools: %s\n", tools)
	}

	b.WriteString("\n## Path rules\n\n")
	b.WriteString("- Bundled skill files are relative to **Root**, not the project WorkDir.\n")
	b.WriteString("- Prefer the **absolute** paths listed below for View/Bash/Grep.\n")
	b.WriteString("- Skill-relative forms (`scripts/…`, `references/…`, `assets/…`) also resolve against skill roots.\n")
	b.WriteString("- Project source files still use WorkDir-relative paths as usual.\n")

	b.WriteString("\n## Instructions\n\n")
	b.WriteString(body)
	b.WriteString("\n")

	if def.IsPackage() {
		bundled := def.ListBundledFiles()
		b.WriteString("\n## Bundled resources\n\n")
		b.WriteString("Load only what you need (progressive disclosure).\n")
		writeAbsResourceList(&b, root, "scripts", bundled.Scripts, "Run with Bash when instructions call for it.")
		writeAbsResourceList(&b, root, "references", bundled.References, "Read with View/Grep when you need detail.")
		writeAbsResourceList(&b, root, "assets", bundled.Assets, "Use as templates or static inputs when referenced.")
		if len(bundled.Scripts) == 0 && len(bundled.References) == 0 && len(bundled.Assets) == 0 {
			b.WriteString("- (no scripts/, references/, or assets/ files found under Root)\n")
		}
	}

	return Result(strings.TrimSpace(b.String()) + "\n"), nil
}

func writeAbsResourceList(b *strings.Builder, root, label string, files []string, hint string) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(b, "### %s/\n", label)
	if hint != "" {
		fmt.Fprintf(b, "%s\n", hint)
	}
	for _, rel := range files {
		abs := rel
		if root != "" {
			abs = filepath.Join(root, filepath.FromSlash(rel))
		}
		fmt.Fprintf(b, "- %s\n  abs: %s\n", rel, abs)
	}
}
