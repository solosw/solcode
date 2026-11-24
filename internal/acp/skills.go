package acp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/solosw/solcode/internal/skill"
	"github.com/solosw/solcode/internal/tool"
)

// skillCommands uses the same enabled registry as the Skill tool. ACP v1 has
// no skill command discriminator; reserve builtin names and qualify collisions.
func (sess *acpSession) skillCommands() map[string]skill.Definition {
	commands := make(map[string]skill.Definition)
	if sess == nil || sess.application == nil {
		return commands
	}
	defs := sess.application.SkillRegistry.All()
	reserved := make(map[string]bool)
	for _, def := range defs {
		reserved[def.Name] = true
	}
	for _, def := range defs {
		if def.UserInvocable != nil && !*def.UserInvocable {
			continue
		}
		if !isSlashCommandName(def.Name) {
			continue
		}
		name := def.Name
		for isBuiltinSlashCommand(name) || (name != def.Name && reserved[name]) {
			name = "skill." + name
		}
		for {
			if _, exists := commands[name]; !exists {
				break
			}
			name = "skill." + name
		}
		commands[name] = def
	}
	return commands
}

func (sess *acpSession) availableCommands() []AvailableCommand {
	commands := availableCommands()
	skills := sess.skillCommands()
	// Registry order is stable, as is the command list sent to clients.
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		def := skills[name]
		commands = append(commands, AvailableCommand{Name: name, Description: def.Description, Input: &AvailableCommandInput{Hint: "optional instructions"}})
	}
	return commands
}

func (s *Server) emitAvailableCommands(sess *acpSession) {
	if s.isV2() {
		s.notifyV2(sess.id, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": s.v2Commands(sess)})
		return
	}
	s.emitUpdate(sess.id, SessionUpdate{SessionUpdate: "available_commands_update", AvailableCommands: sess.availableCommands()})
}

func (sess *acpSession) expandSkillCommand(prompt string) (string, error) {
	cmd, ok := parseSlashCommand(prompt)
	if !ok {
		return prompt, nil
	}
	def, ok := sess.skillCommands()[cmd.Name]
	if !ok {
		return prompt, nil
	}
	if _, err := def.ReadInstructions(); err != nil {
		return "", fmt.Errorf("load skill %q: %w", def.Name, err)
	}
	activation := tool.RenderSkillActivation(def, cmd.Args)
	return strings.Join([]string{prompt, "The user explicitly invoked this skill for this request. Its instructions are already loaded below; follow them and use the skill Root for bundled resources.", activation}, "\n\n"), nil
}
