package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/solosw/codeplus-agent/internal/agent"
	"github.com/solosw/codeplus-agent/internal/app"
	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/engine"
	"github.com/solosw/codeplus-agent/internal/permission"
	"github.com/solosw/codeplus-agent/internal/session"
	"github.com/solosw/codeplus-agent/internal/tui"
)

func main() {
	var configPath string
	var prompt string
	var workDir string
	var maxTurns int
	var timeout time.Duration

	var modelOverride string

	flag.StringVar(&configPath, "config", "", "Path to JSON config file")
	flag.StringVar(&prompt, "prompt", "", "Prompt to run non-interactively")
	flag.StringVar(&workDir, "workdir", "", "Working directory for tool execution")
	flag.IntVar(&maxTurns, "max-turns", 0, "Maximum model/tool loop turns")
	flag.DurationVar(&timeout, "timeout", 30*time.Minute, "Maximum run duration")
	flag.StringVar(&modelOverride, "model", "", "Override model (name or ID from config)")
	flag.Parse()

	if prompt == "" && flag.NArg() > 0 {
		prompt = flag.Arg(0)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if workDir != "" {
		cfg.WorkDir = workDir
	}
	if modelOverride != "" {
		cfg.Model = modelOverride
		if err := cfg.Normalize(); err != nil {
			fmt.Fprintf(os.Stderr, "model override: %v\n", err)
			os.Exit(1)
		}
	}

	if prompt == "" {
		if err := runInteractive(cfg, configPath, timeout, maxTurns); err != nil {
			fmt.Fprintf(os.Stderr, "run tui: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := runBatch(cfg, prompt, timeout, maxTurns); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runBatch(cfg config.Config, prompt string, timeout time.Duration, maxTurns int) error {
	application, err := app.New(cfg)
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}
	defer func() { _ = application.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := application.RunPrompt(ctx, prompt, cfg.WorkDir, maxTurns)
	if err != nil {
		return fmt.Errorf("run prompt: %w", err)
	}
	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	fmt.Print(result.Output)
	if result.Output != "" && result.Output[len(result.Output)-1] != '\n' {
		fmt.Println()
	}
	return nil
}

func runInteractive(cfg config.Config, configPath string, timeout time.Duration, maxTurns int) error {
	var program *tea.Program
	persistencePath := config.PersistencePath(configPath, cfg.WorkDir)

	onTextDelta := func(text string) {
		if program != nil {
			program.Send(tui.StreamTextMsg{Text: text})
		}
	}
	onThinkingDelta := func(text string) {
		if program != nil {
			program.Send(tui.StreamThinkingMsg{Text: text})
		}
	}
	onToolStart := func(name string, input json.RawMessage) {
		if program != nil {
			program.Send(tui.ToolStartMsg{Name: name, Input: string(input)})
		}
	}
	onToolDone := func(name string, output string, isError bool) {
		if program != nil {
			program.Send(tui.ToolDoneMsg{Name: name, Output: output, IsError: isError})
		}
	}
	onUsage := func(usage engine.Usage) {
		if program != nil {
			program.Send(tui.TokenUsageMsg{
				InputTokens:              usage.InputTokens,
				OutputTokens:             usage.OutputTokens,
				CacheCreationInputTokens: usage.CacheCreationInputTokens,
				CacheReadInputTokens:     usage.CacheReadInputTokens,
				MaxContextTokens:         usage.MaxContextTokens,
			})
		}
	}

	application, err := app.New(cfg,
		app.WithStreamCallbacks(onTextDelta, onThinkingDelta),
		app.WithToolCallbacks(onToolStart, onToolDone),
		app.WithUsageCallback(onUsage),
	)
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}
	defer func() { _ = application.Close() }()

	application.Coordinator.SetEventHandler(func(event agent.Event) {
		if program == nil {
			return
		}
		output := event.Result.Output
		isError := event.Kind == agent.EventFailed
		if event.Result.Error != "" {
			output = event.Result.Error
			isError = true
		}
		program.Send(tui.AgentStatusMsg{
			ID:          string(event.Status.ID),
			ParentID:    string(event.Status.ParentID),
			Role:        string(event.Status.Role),
			State:       string(event.Status.State),
			Description: event.Description,
			Output:      output,
			IsError:     isError,
		})
	})

	application.Permissions.SetAskFunc(func(toolName, description string) bool {
		if program == nil {
			return false
		}
		responseCh := make(chan bool, 1)
		program.Send(tui.PermissionRequestMsg{
			ToolName:    toolName,
			Description: description,
			ResponseCh:  responseCh,
		})
		select {
		case allowed := <-responseCh:
			return allowed
		case <-time.After(5 * time.Minute):
			return false
		}
	})

	submit := func(prompt string) (tea.Cmd, func()) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := func() tea.Msg {
			defer cancel()
			sessionID := cfg.Session.DefaultSession
			if sessionID == "" {
				sessionID = "main"
			}
			result, err := application.RunPromptWithSession(ctx, sessionID, prompt, cfg.WorkDir, maxTurns)
			if errors.Is(ctx.Err(), context.Canceled) {
				return tui.StreamCanceledMsg{Reason: "Canceled"}
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return tui.StreamErrorMsg{Err: context.DeadlineExceeded}
			}
			if err != nil {
				return tui.StreamErrorMsg{Err: err}
			}
			if result.Error != "" {
				if result.Error == context.Canceled.Error() {
					return tui.StreamCanceledMsg{Reason: "Canceled"}
				}
				return tui.StreamErrorMsg{Err: fmt.Errorf("%s", result.Error)}
			}
			if result.Output != "" && !cfg.Stream {
				program.Send(tui.StreamTextMsg{Text: result.Output})
			}
			return tui.StreamDoneMsg{}
		}
		return cmd, cancel
	}

	model := tui.NewWith(submit, tui.Dark, cfg.Model, cfg.WorkDir, true)
	if application.Sessions != nil {
		sessionName := cfg.Session.DefaultSession
		if sessionName == "" {
			sessionName = "main"
		}
		if s, err := application.Sessions.LoadOrCreate(context.Background(), sessionID(sessionName), cfg.WorkDir, cfg.Model); err == nil && len(s.Messages) > 0 {
			model.ReplaceMessages(chatMessagesFromSession(s))
		}
	}
	model.SetModelNameFn(func() string { return cfg.Model })
	model.SetSlashCommandHandler(func(command, args string) string {
		switch command {
		case "sessions":
			return handleSessionsCommand(&cfg, application)
		case "new-session":
			return handleNewSessionCommand(&cfg, application, args, persistencePath)
		default:
			return fmt.Sprintf("Unknown command: /%s. Try /help.", command)
		}
	})
	modeNames := []string{"auto", "accept_edits", "bypass", "yolo", "plan"}
	model.SetModeSwitchFn(modeNames, func(mode string) {
		permissionMode := permission.Mode(mode)
		application.Permissions.SetMode(permissionMode)
	})
	model.SetDialogCallbacks(func(kind tui.DialogKind) []tui.DialogItem {
		switch kind {
		case tui.DialogModel:
			models := cfg.ListModels()
			items := make([]tui.DialogItem, 0, len(models))
			for _, m := range models {
				label := m.Name
				if m.DisplayName != "" {
					label = m.DisplayName
				}
				subtitle := m.ID
				if m.Provider != "" {
					subtitle += " · " + m.Provider
				}
				if m.Default {
					subtitle += " · default"
				}
				items = append(items, tui.DialogItem{
					Label:    label,
					Subtitle: subtitle,
					Current:  m.Current,
					Value:    m.Name,
				})
			}
			return items
		case tui.DialogProvider:
			providers := make([]tui.DialogItem, 0, len(cfg.Providers))
			for _, p := range cfg.Providers {
				current := cfg.Provider == p.Name
				subtitle := p.Type
				if p.BaseURL != "" {
					subtitle += " · " + p.BaseURL
				}
				providers = append(providers, tui.DialogItem{
					Label:    p.Name,
					Subtitle: subtitle,
					Current:  current,
					Value:    p.Name,
				})
			}
			return providers
		case tui.DialogSessions:
			return sessionDialogItems(&cfg, application)
		}
		return nil
	}, func(kind tui.DialogKind, value string) tui.SelectResult {
		switch kind {
		case tui.DialogModel:
			return tui.SelectResult{Message: handleModelSwitch(&cfg, application, value, persistencePath)}
		case tui.DialogProvider:
			return tui.SelectResult{Message: handleProviderSwitch(&cfg, application, value, persistencePath)}
		case tui.DialogSessions:
			return handleSessionSwitch(&cfg, application, value, persistencePath)
		}
		return tui.SelectResult{Message: fmt.Sprintf("Selected: %s", value)}
	})
	program = tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = program.Run()
	return err
}

func handleModelSwitch(cfg *config.Config, application *app.App, modelValue, persistencePath string) string {
	next, err := cfg.WithModel(modelValue)
	if err != nil {
		return fmt.Sprintf("Could not switch model to %q: %v", modelValue, err)
	}
	if err := application.SwitchModel(next); err != nil {
		return fmt.Sprintf("Could not switch model to %q: %v", modelValue, err)
	}
	*cfg = next
	message := fmt.Sprintf("Switched model to %s. Future prompts will use this model.", cfg.Model)
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"model": modelValue}); err != nil {
		message += fmt.Sprintf("\nWarning: could not persist model selection: %v", err)
	}
	return message
}

func handleProviderSwitch(cfg *config.Config, application *app.App, providerName, persistencePath string) string {
	next, err := cfg.WithProvider(providerName)
	if err != nil {
		return fmt.Sprintf("Could not switch provider to %q: %v", providerName, err)
	}
	if err := application.SwitchModel(next); err != nil {
		return fmt.Sprintf("Could not switch provider to %q: %v", providerName, err)
	}
	*cfg = next
	message := fmt.Sprintf("Switched provider to %s. Model is now %s.", next.Provider, next.Model)
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"provider": providerName}); err != nil {
		message += fmt.Sprintf("\nWarning: could not persist provider selection: %v", err)
	}
	return message
}

func sessionDialogItems(cfg *config.Config, application *app.App) []tui.DialogItem {
	if application == nil || application.Sessions == nil {
		return nil
	}
	metas, err := application.Sessions.List(context.Background())
	if err != nil {
		return nil
	}
	current := cfg.Session.DefaultSession
	if current == "" {
		current = "main"
	}
	items := make([]tui.DialogItem, 0, len(metas))
	for _, meta := range metas {
		subtitle := ""
		if meta.Model != "" {
			subtitle = meta.Model
		}
		if !meta.UpdatedAt.IsZero() {
			if subtitle != "" {
				subtitle += " · "
			}
			subtitle += meta.UpdatedAt.Format("2006-01-02 15:04")
		}
		items = append(items, tui.DialogItem{
			Label:    string(meta.ID),
			Subtitle: subtitle,
			Current:  string(meta.ID) == current,
			Value:    string(meta.ID),
		})
	}
	return items
}

func handleSessionSwitch(cfg *config.Config, application *app.App, sessionName, persistencePath string) tui.SelectResult {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return tui.SelectResult{Message: "Session name is required."}
	}
	if application == nil || application.Sessions == nil {
		return tui.SelectResult{Message: "Sessions are not enabled."}
	}
	s, err := application.Sessions.LoadOrCreate(context.Background(), sessionID(sessionName), cfg.WorkDir, cfg.Model)
	if err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not switch session to %q: %v", sessionName, err)}
	}
	cfg.Session.DefaultSession = sessionName
	application.Config.Session.DefaultSession = sessionName
	messages := chatMessagesFromSession(s)
	message := persistDefaultSessionMessage(fmt.Sprintf("Switched session to %s.", sessionName), persistencePath, sessionName)
	return tui.SelectResult{
		Message:         message,
		Messages:        messages,
		ReplaceMessages: true,
	}
}

func chatMessagesFromSession(s *session.Session) []tui.ChatMessage {
	if s == nil {
		return nil
	}
	messages := []tui.ChatMessage{{Role: "system", Content: fmt.Sprintf("Loaded session: %s", s.Metadata.ID), TimeStamp: time.Now()}}
	for _, msg := range s.Messages {
		role := string(msg.Role)
		for _, block := range msg.Content {
			switch {
			case block.OfText != nil:
				text := strings.TrimSpace(block.OfText.Text)
				if text == "" {
					continue
				}
				displayRole := "assistant"
				if role == "user" {
					displayRole = "user"
				} else if role == "system" {
					displayRole = "system"
				}
				messages = append(messages, tui.ChatMessage{Role: displayRole, Content: text, TimeStamp: time.Now()})
			case block.OfToolUse != nil:
				input := ""
				if raw, err := json.Marshal(block.OfToolUse.Input); err == nil {
					input = string(raw)
				}
				messages = append(messages, tui.ChatMessage{
					Role:      "tool",
					ToolName:  block.OfToolUse.Name,
					Content:   input,
					TimeStamp: time.Now(),
				})
			case block.OfToolResult != nil:
				text := toolResultText(block.OfToolResult)
				messages = append(messages, tui.ChatMessage{
					Role:      "tool-done",
					ToolName:  "Result",
					Content:   text,
					Collapsed: true,
					TimeStamp: time.Now(),
				})
			}
		}
	}
	return messages
}

func toolResultText(result *sdk.ToolResultBlockParam) string {
	var parts []string
	for _, content := range result.Content {
		if content.OfText != nil {
			parts = append(parts, content.OfText.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func handleSessionsCommand(cfg *config.Config, application *app.App) string {
	if application == nil || application.Sessions == nil {
		return "Sessions are not enabled."
	}
	metas, err := application.Sessions.List(context.Background())
	if err != nil {
		return fmt.Sprintf("Could not list sessions: %v", err)
	}
	current := cfg.Session.DefaultSession
	if current == "" {
		current = "main"
	}
	if len(metas) == 0 {
		return fmt.Sprintf("No saved sessions yet. Current session: %s", current)
	}
	lines := []string{fmt.Sprintf("Current session: %s", current), "", "Sessions:"}
	for _, meta := range metas {
		marker := " "
		if string(meta.ID) == current {
			marker = "*"
		}
		updated := ""
		if !meta.UpdatedAt.IsZero() {
			updated = " · " + meta.UpdatedAt.Format("2006-01-02 15:04")
		}
		model := ""
		if meta.Model != "" {
			model = " · " + meta.Model
		}
		lines = append(lines, fmt.Sprintf("%s %s%s%s", marker, meta.ID, model, updated))
	}
	lines = append(lines, "", "Use /new-session [name] to create and switch sessions.")
	return strings.Join(lines, "\n")
}

func handleNewSessionCommand(cfg *config.Config, application *app.App, args, persistencePath string) string {
	if application == nil || application.Sessions == nil {
		return "Sessions are not enabled."
	}
	name := strings.TrimSpace(args)
	if name == "" {
		name = "session-" + time.Now().Format("20060102-150405")
	}
	name = sanitizeSessionName(name)
	if name == "" {
		return "Session name is required."
	}
	s, err := application.Sessions.LoadOrCreate(context.Background(), sessionID(name), cfg.WorkDir, cfg.Model)
	if err != nil {
		return fmt.Sprintf("Could not create session %q: %v", name, err)
	}
	s.Metadata.Title = name
	if err := application.Sessions.Save(context.Background(), s); err != nil {
		return fmt.Sprintf("Could not save session %q: %v", name, err)
	}
	cfg.Session.DefaultSession = name
	application.Config.Session.DefaultSession = name
	return persistDefaultSessionMessage(fmt.Sprintf("Started new session: %s", name), persistencePath, name)
}

func persistDefaultSessionMessage(message, persistencePath, sessionName string) string {
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{
		"session": map[string]any{"default_session": sessionName},
	}); err != nil {
		message += fmt.Sprintf("\nWarning: could not persist default session: %v", err)
	}
	return message
}

func sanitizeSessionName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func sessionID(value string) session.SessionID {
	return session.SessionID(value)
}
