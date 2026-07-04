package tui

import (
	"fmt"
	"strings"
	"time"
)

type SlashCommandHandler func(command, args string) string

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
		"/sessions — list saved sessions",
		"/new-session [name] — create and switch to a new session",
	}, "\n")
}

func (m *Model) handleSlashCommand(input string) bool {
	cmd, ok := parseSlashCommand(input)
	if !ok {
		return false
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
	case "sessions":
		if m.itemsFunc == nil {
			m.appendCommandResult("/sessions is not available in this session.")
		} else {
			m.ShowDialog(DialogSessions)
		}
	case "new-session":
		if m.slashHandler == nil {
			m.appendCommandResult("/new-session is not available in this session.")
		} else {
			result := m.slashHandler(cmd.Name, cmd.Args)
			m.messages = []ChatMessage{m.systemMessage(result)}
		}
	default:
		m.appendCommandResult(fmt.Sprintf("Unknown command: /%s. Try /help.", cmd.Name))
	}
	m.status = "Ready"
	m.refreshViewport()
	return true
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
