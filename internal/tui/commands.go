package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/solosw/solcode/internal/httpproxy"
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
	// Only a single leading slash introduces a command. "//..." and bare "/"
	// are ordinary chat (e.g. comments, paths, or incomplete typing).
	if !strings.HasPrefix(input, "/") || strings.HasPrefix(input, "//") || input == "/" {
		return slashCommand{}, false
	}
	body := strings.TrimSpace(strings.TrimPrefix(input, "/"))
	if body == "" {
		return slashCommand{}, false
	}
	name, args, _ := strings.Cut(body, " ")
	name = strings.ToLower(strings.TrimSpace(name))
	args = strings.TrimSpace(args)
	if !isSlashCommandName(name) {
		return slashCommand{}, false
	}
	return slashCommand{Name: name, Args: args}, true
}

// isSlashCommandName reports whether name is a plausible /command token.
// Unknown-but-well-formed names may still fall through to chat later.
func isSlashCommandName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			// ok
		case r == '-' || r == '_' || r == '.':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func slashHelpText() string {
	return strings.Join([]string{
		"Available commands:",
		"/help — show this help",
		"/status — show model, context, and cache usage",
		"/clear — clear the current TUI transcript",
		"/model — select a model from the current provider",
		"/provider — select a provider via dialog",
		"/effort — select thinking effort via dialog",
		"/sessions — list saved sessions",
		"/compact — compact the current session now",
		"/checkpoints — list code checkpoints for the current session",
		"/checkpoint-name <name> [turn] — label a checkpoint (default: newest)",
		"/rewind <turn|name> — restore workspace files to a previous turn (code only)",
		"/fix-session — repair invalid tool-use chains in the current session",
		"/new-session [name] — create and switch to a new session",
		"/skills — browse skills and toggle enabled/disabled",
		"/mcp — browse MCP servers and toggle enabled/disabled",
		"/proxy [url|on|off|clear] — show or set HTTP(S) proxy",
		"/goal [description] — work from goal.md until complete",
		"/workflows — list loaded workflows (explicit Task graphs)",
		"/workflow <name> [args] — run workflow; /[name]-workflow is its shortcut",
		"/workflow-edit — terminal workflow orchestrator (list/edit tasks)",
		"/web-ui — open Dify-style web node editor (browser)",
		"/[skill] [args] — invoke a loaded skill by name",
	}, "\n")
}

func (m *Model) handleSlashCommand(input string) (bool, tea.Cmd) {
	cmd, ok := parseSlashCommand(input)
	if !ok {
		return false, nil
	}
	// Direct workflow shortcut: /ppt-workflow args → same as /workflow ppt args
	// (slash command always uses <workflow-name>-workflow; workflow name itself need not include the suffix).
	if !isBuiltinSlashCommand(cmd.Name) {
		if workflowName, ok := m.resolveDirectWorkflowName(cmd.Name); ok {
			m.messages = append(m.messages, ChatMessage{Role: "user", Content: input, TimeStamp: time.Now()})
			if m.slashAsyncHandler == nil {
				m.appendCommandResult(fmt.Sprintf("/%s is not available in this session.", cmd.Name))
				m.status = "Ready"
				m.refreshViewport()
				return true, nil
			}
			m.permissionMode = "bypass"
			m.status = fmt.Sprintf("Running workflow %s...", workflowName)
			m.spinnerActive = true
			m.loadingStart = time.Now()
			m.refreshViewport()
			payload := workflowName
			if strings.TrimSpace(cmd.Args) != "" {
				payload = workflowName + " " + strings.TrimSpace(cmd.Args)
			}
			return true, tea.Batch(m.slashAsyncHandler("workflow", payload), m.nextSpinnerTick())
		}
	}
	if !isBuiltinSlashCommand(cmd.Name) {
		// Skills keep the special "/skill args" → Skill tool rewrite path.
		if m.isSkillName(cmd.Name) {
			return false, nil
		}
		// Anything else that looks like /foo but isn't a real command is just chat
		// (paths, prose, typos). Do not intercept with "Unknown command".
		return false, nil
	}
	m.messages = append(m.messages, ChatMessage{Role: "user", Content: input, TimeStamp: time.Now()})
	switch cmd.Name {
	case "help":
		m.appendCommandResult(slashHelpText())
	case "status":
		m.appendCommandResult(m.slashStatusText())
	case "clear":
		m.messages = []ChatMessage{m.systemMessage("Conversation cleared. Type /help for commands.")}
		m.resetSessionTokenTotals()
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
	case "proxy":
		if m.slashHandler == nil {
			m.appendCommandResult("/proxy is not available in this session.")
		} else {
			m.appendCommandResult(m.slashHandler(cmd.Name, cmd.Args))
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
	case "checkpoints", "checkpoint-name", "rewind":
		if m.slashAsyncHandler == nil {
			m.appendCommandResult(fmt.Sprintf("/%s is not available in this session.", cmd.Name))
		} else {
			switch cmd.Name {
			case "checkpoints":
				m.status = "Listing checkpoints..."
			case "checkpoint-name":
				m.status = "Naming checkpoint..."
			default:
				m.status = "Rewinding files..."
			}
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
	case "goal":
		if m.slashAsyncHandler == nil {
			m.appendCommandResult("/goal is not available in this session.")
		} else {
			m.status = "Preparing goal..."
			m.spinnerActive = true
			m.loadingStart = time.Now()
			m.refreshViewport()
			return true, tea.Batch(m.slashAsyncHandler(cmd.Name, cmd.Args), m.nextSpinnerTick())
		}
	case "workflows":
		if m.slashHandler == nil {
			m.appendCommandResult("/workflows is not available in this session.")
		} else {
			m.appendCommandResult(m.slashHandler(cmd.Name, cmd.Args))
		}
	case "workflow-edit":
		m.ShowWorkflowEditor()
	case "web-ui":
		if m.workflowUIHandler == nil {
			m.appendCommandResult("/web-ui is not available in this session.")
		} else {
			m.appendCommandResult(m.workflowUIHandler())
		}
	case "workflow":
		if m.slashAsyncHandler == nil {
			m.appendCommandResult("/workflow is not available in this session.")
		} else if strings.TrimSpace(cmd.Args) == "" {
			m.appendCommandResult("Usage: /workflow <name> [args]\nList loaded workflows with /workflows.")
		} else {
			name, rest, _ := strings.Cut(strings.TrimSpace(cmd.Args), " ")
			m.permissionMode = "bypass"
			m.status = fmt.Sprintf("Running workflow %s...", name)
			m.spinnerActive = true
			m.loadingStart = time.Now()
			m.refreshViewport()
			// Pass "name [args]" so the async handler can split them.
			payload := name
			if strings.TrimSpace(rest) != "" {
				payload = name + " " + strings.TrimSpace(rest)
			}
			return true, tea.Batch(m.slashAsyncHandler(cmd.Name, payload), m.nextSpinnerTick())
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
		// Builtins are listed exhaustively above; treat gaps as chat, not errors.
		return false, nil
	}
	m.status = "Ready"
	m.refreshViewport()
	return true, nil
}

func (m *Model) slashStatusText() string {
	usage := m.tokenUsage
	used := m.displayContextTokens()
	limit := m.currentContextLimit()
	inputSide := usageInputSideTotal(usage.InputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
	cacheRead := max64(0, usage.CacheReadInputTokens)
	cacheWrite := max64(0, usage.CacheCreationInputTokens)
	cacheUsed := cacheRead + cacheWrite
	outTokens := max64(0, usage.OutputTokens)

	modelName := strings.TrimSpace(m.currentModelName())
	if modelName == "" {
		modelName = "solcode"
	}

	lines := []string{
		fmt.Sprintf("Model: %s", modelName),
	}
	if mode := strings.TrimSpace(m.permissionMode); mode != "" {
		lines = append(lines, fmt.Sprintf("Mode: %s", mode))
	}
	if cwd := strings.TrimSpace(m.cwd); cwd != "" {
		lines = append(lines, fmt.Sprintf("Workdir: %s", cwd))
	}
	// Prefer live process proxy state so /proxy takes effect immediately in /status.
	lines = append(lines, proxyStatusForTUI())
	lines = append(lines,
		fmt.Sprintf("Context: %s / %s (%s)", compactTokens(used), renderContextLimit(limit), tokenSharePercent(used, limit)),
		fmt.Sprintf("Cache: %s / %s input-side (%s) — read %s, write %s",
			compactTokens(cacheUsed),
			compactTokens(inputSide),
			tokenSharePercent(cacheUsed, inputSide),
			compactTokens(cacheRead),
			compactTokens(cacheWrite),
		),
		fmt.Sprintf("Output: %s (session total)", compactTokens(outTokens)),
		fmt.Sprintf("Input: %s uncached (session total)", compactTokens(max64(0, usage.InputTokens))),
	)
	return strings.Join(lines, "\n")
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

const workflowSlashSuffix = "-workflow"

// workflowSlashCommand returns the slash command form for a workflow name.
// Example: "ppt" → "ppt-workflow"; "ppt-workflow" → "ppt-workflow".
func workflowSlashCommand(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	if strings.HasSuffix(name, workflowSlashSuffix) {
		return name
	}
	return name + workflowSlashSuffix
}

// resolveDirectWorkflowName maps a slash command like "ppt-workflow" to a loaded
// workflow name. Prefers an exact name match, then the base name without the
// "-workflow" suffix (so workflow "ppt" is invoked by /ppt-workflow).
func (m *Model) resolveDirectWorkflowName(slashName string) (string, bool) {
	slashName = strings.TrimSpace(strings.ToLower(slashName))
	if slashName == "" || !strings.HasSuffix(slashName, workflowSlashSuffix) || m.workflowNamesFn == nil {
		return "", false
	}
	names := m.workflowNamesFn()
	for _, workflowName := range names {
		if workflowName == slashName {
			return workflowName, true
		}
	}
	base := strings.TrimSuffix(slashName, workflowSlashSuffix)
	if base == "" {
		return "", false
	}
	for _, workflowName := range names {
		if workflowName == base {
			return workflowName, true
		}
	}
	return "", false
}

func (m *Model) isDirectWorkflowCommand(name string) bool {
	_, ok := m.resolveDirectWorkflowName(name)
	return ok
}

// directWorkflowSlashCommands lists slash command names for autocomplete.
func (m *Model) directWorkflowSlashCommands() []string {
	if m.workflowNamesFn == nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, name := range m.workflowNamesFn() {
		cmd := workflowSlashCommand(name)
		if cmd == "" || isBuiltinSlashCommand(cmd) {
			continue
		}
		if _, ok := seen[cmd]; ok {
			continue
		}
		seen[cmd] = struct{}{}
		out = append(out, cmd)
	}
	return out
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
	"help":          true,
	"status":        true,
	"clear":         true,
	"model":         true,
	"provider":      true,
	"effort":        true,
	"sessions":      true,
	"compact":          true,
	"checkpoints":      true,
	"checkpoint-name":  true,
	"rewind":           true,
	"fix-session":      true,
	"new-session":   true,
	"skills":        true,
	"mcp":           true,
	"proxy":         true,
	"goal":          true,
	"workflows":     true,
	"workflow":      true,
	"workflow-edit": true,
	"web-ui":        true,
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

func proxyStatusForTUI() string {
	return httpproxy.StatusLine(httpproxy.Get())
}

func (m *Model) applySelectResult(result SelectResult) {
	if result.ReplaceMessages {
		m.messages = result.Messages
		// Switching sessions resets in-memory totals; restore from SelectResult if provided.
		m.resetSessionTokenTotals()
	}
	if result.TokenUsage != nil {
		m.applyTokenUsage(*result.TokenUsage)
	}
	if strings.TrimSpace(result.Message) != "" {
		m.messages = append(m.messages, m.systemMessage(result.Message))
	}
	m.status = "Ready"
	m.refreshViewport()
}
