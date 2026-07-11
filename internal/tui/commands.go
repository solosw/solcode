package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type SlashCommandHandler func(command, args string) string
type SlashCommandAsyncHandler func(command, args string) tea.Cmd

type NewSessionHandler func(name string, crossSessionMemory bool) SelectResult

type slashCommand struct {
	Name string
	Args string
}

func parseSlashCommand(input string) (slashCommand, bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") || input == "/" {
		return slashCommand{}, false
	}
	body := strings.TrimSpace(strings.TrimPrefix(input, "/"))
	if body == "" {
		return slashCommand{}, false
	}
	name, args, _ := strings.Cut(body, " ")
	name = strings.ToLower(strings.TrimSpace(name))
	args = strings.TrimSpace(args)
	if name == "" {
		return slashCommand{}, false
	}
	return slashCommand{Name: name, Args: args}, true
}

func slashHelpText() string {
	return strings.Join([]string{
		"Available commands:",
		"/help — show this help",
		"/clear — clear the current TUI transcript",
		"/model — select a model via dialog",
		"/provider — select a provider via dialog",
		"/effort — select thinking effort via dialog",
		"/sessions — list saved sessions",
		"/compact — compact the current session now",
		"/fix-session — repair invalid tool-use chains in the current session",
		"/new-session [name] — create and switch to a new session",
		"/skills — browse skills and toggle enabled/disabled",
		"/mcp — browse MCP servers and toggle enabled/disabled",
		"/[skill] [args] — invoke a loaded skill by name",
	}, "\n")
}

func (m *Model) handleSlashCommand(input string) (bool, tea.Cmd) {
	cmd, ok := parseSlashCommand(input)
	if !ok {
		return false, nil
	}
	if !isBuiltinSlashCommand(cmd.Name) && m.isSkillName(cmd.Name) {
		return false, nil
	}
	m.messages = append(m.messages, ChatMessage{Role: "user", Content: input, TimeStamp: time.Now()})
	switch cmd.Name {
	case "help":
		m.appendCommandResult(slashHelpText())
	case "clear":
		m.messages = []ChatMessage{m.systemMessage("Conversation cleared. Type /help for commands.")}
	case "model":
		if m.itemsFunc == nil {
			m.appendCommandResult("/model is not available in this session.")
		} else {
			m.ShowDialog(DialogModel)
		}
	case "provider":
		if m.itemsFunc == nil {
			m.appendCommandResult("/provider is not available in this session.")
		} else {
			m.ShowDialog(DialogProvider)
		}
	case "effort":
		if m.itemsFunc == nil {
			m.appendCommandResult("/effort is not available in this session.")
		} else {
			m.ShowDialog(DialogEffort)
		}
	case "sessions":
		if m.itemsFunc == nil {
			m.appendCommandResult("/sessions is not available in this session.")
		} else {
			m.ShowDialog(DialogSessions)
		}
	case "skills":
		if m.itemsFunc == nil {
			m.appendCommandResult("/skills is not available in this session.")
		} else {
			m.ShowDialog(DialogSkills)
		}
	case "mcp":
		if m.itemsFunc == nil {
			m.appendCommandResult("/mcp is not available in this session.")
		} else {
			m.ShowDialog(DialogMCP)
		}
	case "compact":
		if m.slashAsyncHandler == nil {
			m.appendCommandResult("/compact is not available in this session.")
		} else {
			m.status = "Compacting..."
			m.spinnerActive = true
			m.loadingStart = time.Now()
			m.refreshViewport()
			return true, tea.Batch(m.slashAsyncHandler(cmd.Name, cmd.Args), m.nextSpinnerTick())
		}
	case "fix-session":
		if m.slashAsyncHandler == nil {
			m.appendCommandResult("/fix-session is not available in this session.")
		} else {
			m.status = "Repairing session..."
			m.spinnerActive = true
			m.loadingStart = time.Now()
			m.refreshViewport()
			return true, tea.Batch(m.slashAsyncHandler(cmd.Name, cmd.Args), m.nextSpinnerTick())
		}
	case "new-session":
		if m.newSessionHandler == nil {
			m.appendCommandResult(fmt.Sprintf("/%s is not available in this session.", cmd.Name))
		} else {
			name := strings.TrimSpace(cmd.Args)
			if name == "" {
				name = "session-" + time.Now().Format("20060102-150405")
			}
			question := fmt.Sprintf("Enable cross-session memory for %q? Memories from other sessions will be available in this session.", name)
			handler := m.newSessionHandler
			m.ShowConfirm(question, func(confirmed bool) SelectResult {
				return handler(name, confirmed)
			})
		}
	default:
		m.appendCommandResult(fmt.Sprintf("Unknown command: /%s. Try /help.", cmd.Name))
	}
	m.status = "Ready"
	m.refreshViewport()
	return true, nil
}

func (m *Model) isSkillName(name string) bool {
	if m.skillNamesFn == nil {
		return false
	}
	for _, skillName := range m.skillNamesFn() {
		if skillName == name {
			return true
		}
	}
	return false
}

func (m *Model) slashSkillPrompt(input string) (string, bool) {
	cmd, ok := parseSlashCommand(input)
	if !ok || isBuiltinSlashCommand(cmd.Name) || !m.isSkillName(cmd.Name) {
		return "", false
	}
	if cmd.Args == "" {
		return fmt.Sprintf("Use the Skill tool with skill %q.", cmd.Name), true
	}
	return fmt.Sprintf("Use the Skill tool with skill %q and args %q.", cmd.Name, cmd.Args), true
}

var builtinCommands = map[string]bool{
	"help":        true,
	"clear":       true,
	"model":       true,
	"provider":    true,
	"effort":      true,
	"sessions":    true,
	"compact":     true,
	"fix-session": true,
	"new-session": true,
	"skills":      true,
	"mcp":         true,
}

func isBuiltinSlashCommand(name string) bool {
	return builtinCommands[name]
}

func (m Model) systemMessage(content string) ChatMessage {
	return ChatMessage{Role: "system", Content: content, TimeStamp: time.Now()}
}

func (m *Model) appendCommandResult(content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		content = "(no output)"
	}
	m.messages = append(m.messages, m.systemMessage(content))
}

func (m *Model) applySelectResult(result SelectResult) {
	if result.ReplaceMessages {
		m.messages = result.Messages
	}
	if strings.TrimSpace(result.Message) != "" {
		m.messages = append(m.messages, m.systemMessage(result.Message))
	}
	m.status = "Ready"
	m.refreshViewport()
}
