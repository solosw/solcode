package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/solosw/codeplus-agent/internal/skill"
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
	return `Execute a configured skill file within the conversation.
Use this when a task matches a reusable project or user workflow defined in a markdown skill.`
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
	content, err := os.ReadFile(def.Path)
	if err != nil {
		return ErrorResult("read skill: " + err.Error()), nil
	}
	text := strings.TrimSpace(string(content))
	if text == "" {
		text = fmt.Sprintf("Skill %s is empty", def.Name)
	}
	if params.Args != "" {
		text = fmt.Sprintf("[Skill: %s]\nArgs: %s\n\n%s", def.Name, params.Args, text)
	} else {
		text = fmt.Sprintf("[Skill: %s]\n\n%s", def.Name, text)
	}
	return Result(text), nil
}
