package acp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/solosw/solcode/internal/skill"
	"github.com/solosw/solcode/internal/tool"
)

type v2Command struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Path        string            `json:"path,omitempty"`
	Input       map[string]string `json:"input,omitempty"`
}

func (s *Server) v2Commands(sess *acpSession) []v2Command {
	s.mu.Lock()
	structured := s.v2SkillCommands
	s.mu.Unlock()
	var commands []v2Command
	for _, cmd := range availableCommands() {
		commands = append(commands, v2TextCommand(cmd))
	}
	skills := sess.skillCommands()
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)
	advertised := make(map[string]skill.Definition, len(names))
	for _, name := range names {
		def := skills[name]
		if structured {
			cmd := v2Command{Type: "skill", Name: name, Description: def.Description}
			if def.IsPackage() && filepath.IsAbs(def.Path) {
				cmd.Path = def.Path
			}
			commands = append(commands, cmd)
			advertised[name] = def
		} else {
			commands = append(commands, v2TextCommand(AvailableCommand{Name: name, Description: def.Description, Input: &AvailableCommandInput{Hint: "optional instructions"}}))
		}
	}
	sess.mu.Lock()
	sess.v2Skills = advertised
	sess.mu.Unlock()
	return commands
}

func v2TextCommand(cmd AvailableCommand) v2Command {
	result := v2Command{Type: "command", Name: cmd.Name, Description: cmd.Description}
	if cmd.Input != nil {
		result.Input = map[string]string{"type": "text", "hint": cmd.Input.Hint}
	}
	return result
}

func (sess *acpSession) v2PromptText(blocks []ContentBlock) (string, bool, error) {
	sess.mu.Lock()
	advertised := sess.v2Skills
	sess.mu.Unlock()
	var active map[string]skill.Definition
	rendered := append([]ContentBlock(nil), blocks...)
	inline := false
	for i, block := range blocks {
		switch block.Type {
		case "text", "image", "resource", "resource_link":
		case "skill":
			inline = true
			if active == nil {
				active = sess.skillCommands()
			}
			def, enabled := active[block.Name]
			published, known := advertised[block.Name]
			if !known || !enabled || published.Path != def.Path || published.Name != def.Name {
				return "", false, invalidConfigError("unknown or unavailable skill %q", block.Name)
			}
			if _, err := def.ReadInstructions(); err != nil {
				return "", false, fmt.Errorf("load skill %q: %w", def.Name, err)
			}
			rendered[i] = ContentBlock{Type: "text", Text: "The user explicitly invoked this skill for this request. Follow its instructions and use the skill Root for bundled resources.\n\n" + tool.RenderSkillActivation(def, "")}
		default:
			return "", false, invalidConfigError("unsupported prompt content type %q", block.Type)
		}
	}
	prompt := strings.TrimSpace(promptToText(rendered, sess.workDir))
	if prompt == "" {
		return "", false, invalidConfigError("prompt is required")
	}
	return prompt, inline, nil
}
