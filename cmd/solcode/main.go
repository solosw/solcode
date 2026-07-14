package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/skill"
	"github.com/solosw/solcode/internal/tool"
	"github.com/solosw/solcode/internal/tui"
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
	flag.DurationVar(&timeout, "timeout", 0, "Maximum run duration (0 disables the timeout)")
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

// conversationContext creates a cancelable context. A non-positive timeout
// intentionally means no deadline, so long-running conversations continue
// until they finish or the user cancels them.
func conversationContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}

func runBatch(cfg config.Config, prompt string, timeout time.Duration, maxTurns int) error {
	application, err := app.New(cfg)
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}
	defer func() { _ = application.Close() }()

	ctx, cancel := conversationContext(timeout)
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
	var queuedPrompts struct {
		sync.Mutex
		items []string
	}
	queuePrompt := func(prompt string) {
		prompt = strings.TrimSpace(prompt)
		if prompt == "" {
			return
		}
		queuedPrompts.Lock()
		queuedPrompts.items = append(queuedPrompts.items, prompt)
		queuedPrompts.Unlock()
	}
	drainQueuedPrompts := func() []string {
		queuedPrompts.Lock()
		defer queuedPrompts.Unlock()
		items := append([]string(nil), queuedPrompts.items...)
		queuedPrompts.items = nil
		return items
	}
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
	sendUsage := func(usage engine.Usage) {
		if program == nil {
			return
		}
		program.Send(tui.TokenUsageMsg{
			EstimatedContextTokens:   usage.EstimatedContextTokens,
			InputTokens:              usage.InputTokens,
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
			MaxContextTokens:         usage.MaxContextTokens,
		})
	}
	onUsage := func(usage engine.Usage) {
		sendUsage(usage)
	}
	onStatus := func(status string) {
		if program != nil {
			program.Send(tui.StatusTextMsg{Text: status})
		}
	}
	onAskUser := func(ctx context.Context, params tool.AskUserParams) (map[string]string, error) {
		if program == nil {
			return nil, fmt.Errorf("AskUser is not available outside interactive TUI")
		}
		responseCh := make(chan map[string]string, 1)
		program.Send(tui.AskUserRequestMsg{
			Questions:  askUserQuestionsToTUI(params.Questions),
			ResponseCh: responseCh,
		})
		select {
		case answers := <-responseCh:
			return answers, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	application, err := app.New(cfg,
		app.WithStreamCallbacks(onTextDelta, onThinkingDelta),
		app.WithToolCallbacks(onToolStart, onToolDone),
		app.WithUsageCallback(onUsage),
		app.WithStatusCallback(onStatus),
		app.WithAskUserCallback(onAskUser),
		app.WithQueuedPrompts(drainQueuedPrompts),
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
		ctx, cancel := conversationContext(timeout)
		cmd := func() tea.Msg {
			defer cancel()
			currentSessionID := cfg.Session.DefaultSession
			if currentSessionID == "" {
				currentSessionID = "main"
			}
			result, err := application.RunPromptWithSession(ctx, currentSessionID, prompt, cfg.WorkDir, maxTurns)
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
			if application.Sessions != nil && program != nil {
				if s, loadErr := loadSanitizedSession(context.Background(), application, currentSessionID, cfg); loadErr == nil && s != nil {
					program.Send(tui.ReplaceMessagesMsg{Messages: chatMessagesFromSession(s)})
				}
			}
			if result.Output != "" && !cfg.Stream {
				program.Send(tui.StreamTextMsg{Text: result.Output})
			}
			return tui.StreamDoneMsg{}
		}
		return cmd, cancel
	}

	theme := tui.ThemeByName(cfg.TUI.Theme).WithBackground(cfg.TUI.Background)
	model := tui.NewWith(submit, theme, cfg.Model, cfg.WorkDir, true)
	model.SetQueueFunc(queuePrompt)
	if application.Sessions != nil {
		sessionName := cfg.Session.DefaultSession
		if sessionName == "" {
			sessionName = "main"
		}
		if s, err := loadSanitizedSession(context.Background(), application, sessionName, cfg); err == nil && s != nil && len(s.Messages) > 0 {
			model.ReplaceMessages(chatMessagesFromSession(s))
		}
	}
	model.SetModelNameFn(func() string { return cfg.Model })
	model.SetContextBaseFn(func() int64 {
		builder := engine.ContextBuilder{SystemPrompt: cfg.SystemPrompt, SkillNames: appSkillNames(application)}
		return builder.EstimateContextTokens(engine.BuildRequest{WorkDir: cfg.WorkDir, Tools: application.Tools.All()})
	})
	model.SetContextLimitFn(func() int64 { return cfg.MaxContextTokens })
	model.SetSlashCommandHandler(func(command, args string) string {
		switch command {
		case "sessions":
			return handleSessionsCommand(&cfg, application)
		default:
			return fmt.Sprintf("Unknown command: /%s. Try /help.", command)
		}
	})
	model.SetSlashCommandAsyncHandler(func(command, args string) tea.Cmd {
		switch command {
		case "compact":
			return func() tea.Msg {
				currentSessionID := cfg.Session.DefaultSession
				if currentSessionID == "" {
					currentSessionID = "main"
				}
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				s, changed, err := application.CompactSession(ctx, currentSessionID, cfg.WorkDir)
				if err != nil {
					return tui.CommandResultMsg{Text: fmt.Sprintf("Compact failed: %v", err)}
				}
				if s != nil && program != nil {
					program.Send(tui.ReplaceMessagesMsg{Messages: chatMessagesFromSession(s)})
					sendUsage(usageFromSession(cfg, application, s))
				}
				if !changed {
					return tui.CommandResultMsg{Text: "Compact skipped: current session is below the compaction threshold or has no older segment to compress."}
				}
				return tui.CommandResultMsg{Text: "Compacted current session."}
			}
		case "fix-session":
			return func() tea.Msg {
				currentSessionID := cfg.Session.DefaultSession
				if currentSessionID == "" {
					currentSessionID = "main"
				}
				s, removed, err := application.RepairSession(context.Background(), currentSessionID, cfg.WorkDir)
				if err != nil {
					return tui.CommandResultMsg{Text: fmt.Sprintf("Session repair failed: %v", err)}
				}
				if s != nil && program != nil {
					program.Send(tui.ReplaceMessagesMsg{Messages: chatMessagesFromSession(s)})
					sendUsage(usageFromSession(cfg, application, s))
				}
				if removed == 0 {
					return tui.CommandResultMsg{Text: "Session is already valid; no incomplete tool exchanges were found."}
				}
				return tui.CommandResultMsg{Text: fmt.Sprintf("Repaired current session: removed %d incomplete tool block(s).", removed)}
			}
		default:
			return func() tea.Msg {
				return tui.CommandResultMsg{Text: fmt.Sprintf("Unknown command: /%s. Try /help.", command)}
			}
		}
	})
	model.SetNewSessionHandler(func(name string, crossSessionMemory bool) tui.SelectResult {
		return handleNewSessionCommand(&cfg, application, name, persistencePath, crossSessionMemory)
	})
	modeNames := []string{"auto", "accept_edits", "bypass", "plan"}
	model.SetModeSwitchFn(modeNames, func(mode string) {
		permissionMode := permission.Mode(mode)
		application.Permissions.SetMode(permissionMode)
	})
	model.SetDialogCallbacks(func(kind tui.DialogKind) []tui.DialogItem {
		switch kind {
		case tui.DialogModel:
			models := cfg.ListModels()
			items := make([]tui.DialogItem, 0, len(models)+1)
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
				subtitle := p.BaseURL
				providers = append(providers, tui.DialogItem{
					Label:    p.Name,
					Subtitle: subtitle,
					Current:  current,
					Value:    p.Name,
				})
			}
			return providers
		case tui.DialogEffort:
			efforts := []string{"low", "medium", "high", "xhigh", "max"}
			items := make([]tui.DialogItem, 0, len(efforts))
			for _, effort := range efforts {
				items = append(items, tui.DialogItem{
					Label:   effort,
					Current: cfg.Effort == effort,
					Value:   effort,
				})
			}
			return items
		case tui.DialogSessions:
			return sessionDialogItems(&cfg, application)
		case tui.DialogSkills:
			return skillDialogItems(&cfg, application)
		case tui.DialogMCP:
			return mcpDialogItems(&cfg)
		}
		return nil
	}, func(kind tui.DialogKind, value string) tui.SelectResult {
		switch kind {
		case tui.DialogModel:
			return tui.SelectResult{Message: handleModelSwitch(&cfg, application, value, persistencePath)}
		case tui.DialogProvider:
			return tui.SelectResult{Message: handleProviderSwitch(&cfg, application, value, persistencePath)}
		case tui.DialogEffort:
			return tui.SelectResult{Message: handleEffortSwitch(&cfg, application, value, persistencePath)}
		case tui.DialogSessions:
			return handleSessionSwitch(&cfg, application, value, persistencePath)
		case tui.DialogSkills:
			return handleSkillToggleSelection(&cfg, application, value, persistencePath)
		case tui.DialogMCP:
			return handleMCPToggleSelection(&cfg, application, value, persistencePath)
		}
		return tui.SelectResult{Message: fmt.Sprintf("Selected: %s", value)}
	})
	model.SetCustomDialogCallback(func(kind tui.DialogKind, values []string) tui.SelectResult {
		switch kind {
		case tui.DialogProvider:
			return handleCustomProvider(&cfg, application, values, persistencePath, configPath)
		case tui.DialogModel:
			return handleCustomModel(&cfg, application, values, persistencePath, configPath)
		default:
			return tui.SelectResult{Message: "Custom selection is not supported for this dialog."}
		}
	})
	model.SetSkillNamesFn(func() []string {
		names := make([]string, 0)
		if application.SkillRegistry != nil {
			for _, def := range application.SkillRegistry.All() {
				names = append(names, def.Name)
			}
		}
		return names
	})
	// program = tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	program = tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseAllMotion())
	_, err = program.Run()
	return err
}

func askUserQuestionsToTUI(questions []tool.Question) []tui.AskUserQuestion {
	out := make([]tui.AskUserQuestion, 0, len(questions))
	for _, q := range questions {
		options := make([]tui.AskUserOption, 0, len(q.Options))
		for _, opt := range q.Options {
			options = append(options, tui.AskUserOption{
				Label:       opt.Label,
				Description: opt.Description,
				Preview:     opt.Preview,
			})
		}
		out = append(out, tui.AskUserQuestion{
			Question:    q.Question,
			Header:      q.Header,
			Options:     options,
			MultiSelect: q.MultiSelect,
		})
	}
	return out
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

func handleCustomProvider(cfg *config.Config, application *app.App, values []string, persistencePath, configPath string) tui.SelectResult {
	if len(values) != 3 {
		return tui.SelectResult{Message: "Could not add custom provider: provider name, API key, and base URL are required."}
	}
	name := strings.TrimSpace(values[0])
	apiKey := strings.TrimSpace(values[1])
	baseURL := strings.TrimSpace(values[2])
	if name == "" || apiKey == "" || baseURL == "" {
		return tui.SelectResult{Message: "Could not add custom provider: provider name, API key, and base URL are required."}
	}
	for _, provider := range cfg.Providers {
		if provider.Name == name {
			return tui.SelectResult{Message: fmt.Sprintf("Could not add custom provider: %q already exists.", name)}
		}
	}

	next := *cfg
	next.Providers = append(append([]config.ProviderConfig(nil), cfg.Providers...), config.ProviderConfig{
		Name:    name,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Models:  []config.ModelConfig{},
	})
	if err := next.Normalize(); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not add custom provider %q: %v", name, err)}
	}
	loaded, err := saveAndReloadConfig(next, persistencePath, configPath, map[string]any{"providers": next.Providers})
	if err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not save custom provider %q: %v", name, err)}
	}
	if err := application.SwitchModel(loaded); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not reload custom provider %q: %v", name, err)}
	}
	*cfg = loaded
	return tui.SelectResult{Message: fmt.Sprintf("Added custom provider %s and reloaded settings. Add a model for it with /model.", name)}
}

func handleCustomModel(cfg *config.Config, application *app.App, values []string, persistencePath, configPath string) tui.SelectResult {
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return tui.SelectResult{Message: "Could not add custom model: model ID is required."}
	}
	modelID := strings.TrimSpace(values[0])
	providerName := pendingCustomProviderName(*cfg)
	if providerName == "" {
		providerName = activeProviderName(*cfg)
	}
	if providerName == "" {
		return tui.SelectResult{Message: "Could not add custom model: select a provider first."}
	}

	next := *cfg
	for i := range next.Providers {
		provider := &next.Providers[i]
		if provider.Name != providerName {
			continue
		}
		for _, model := range provider.Models {
			if model.Name == modelID || model.ID == modelID {
				return tui.SelectResult{Message: fmt.Sprintf("Could not add custom model: %q already exists for provider %q.", modelID, providerName)}
			}
		}
		provider.Models = append(provider.Models, config.ModelConfig{Name: modelID, ID: modelID, Provider: providerName})
		break
	}
	next.Provider = providerName
	next.Model = modelID
	if err := next.Normalize(); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not add custom model %q: %v", modelID, err)}
	}
	loaded, err := saveAndReloadConfig(next, persistencePath, configPath, map[string]any{
		"provider":  providerName,
		"model":     modelID,
		"providers": next.Providers,
	})
	if err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not save custom model %q: %v", modelID, err)}
	}
	if err := application.SwitchModel(loaded); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not reload custom model %q: %v", modelID, err)}
	}
	*cfg = loaded
	return tui.SelectResult{Message: fmt.Sprintf("Added custom model %s and reloaded settings. Future prompts will use this model.", modelID)}
}

func activeProviderName(cfg config.Config) string {
	if strings.TrimSpace(cfg.Provider) != "" {
		return cfg.Provider
	}
	for _, provider := range cfg.Providers {
		for _, model := range provider.Models {
			if model.Name == cfg.Model || model.ID == cfg.Model {
				return provider.Name
			}
		}
	}
	if len(cfg.Providers) == 1 {
		return cfg.Providers[0].Name
	}
	return ""
}

func pendingCustomProviderName(cfg config.Config) string {
	for i := len(cfg.Providers) - 1; i >= 0; i-- {
		if len(cfg.Providers[i].Models) == 0 {
			return cfg.Providers[i].Name
		}
	}
	return ""
}

func saveAndReloadConfig(current config.Config, persistencePath, configPath string, updates map[string]any) (config.Config, error) {
	if err := config.SaveLocalOverrides(persistencePath, updates); err != nil {
		return config.Config{}, err
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, err
	}
	loaded.WorkDir = current.WorkDir
	if err := loaded.Normalize(); err != nil {
		return config.Config{}, err
	}
	return loaded, nil
}

func handleEffortSwitch(cfg *config.Config, application *app.App, effort, persistencePath string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Sprintf("Could not switch effort to %q: invalid effort", effort)
	}
	next := *cfg
	next.Effort = effort
	if err := application.SwitchModel(next); err != nil {
		return fmt.Sprintf("Could not switch effort to %q: %v", effort, err)
	}
	*cfg = next
	message := fmt.Sprintf("Switched effort to %s.", effort)
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"effort": effort}); err != nil {
		message += fmt.Sprintf("\nWarning: could not persist effort selection: %v", err)
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

func skillDialogItems(cfg *config.Config, application *app.App) []tui.DialogItem {
	registry := skill.LoadFromDirs(cfg.Skills.Paths...)
	defs := registry.All()
	items := make([]tui.DialogItem, 0, len(defs))
	for _, def := range defs {
		enabled := application != nil && application.SkillRegistry != nil
		if enabled {
			_, enabled = application.SkillRegistry.Find(def.Name)
		}
		status := "disabled"
		if enabled {
			status = "enabled"
		}
		subtitle := fmt.Sprintf("%s · %s", status, def.Source)
		items = append(items, tui.DialogItem{
			Label:    def.Name,
			Subtitle: subtitle,
			Current:  enabled,
			Value:    def.Name,
		})
	}
	return items
}

func mcpDialogItems(cfg *config.Config) []tui.DialogItem {
	items := make([]tui.DialogItem, 0, len(cfg.MCP.Servers))
	for _, server := range cfg.MCP.Servers {
		status := "enabled"
		if server.Disabled {
			status = "disabled"
		}
		detail := server.URL
		if detail == "" {
			detail = server.Command
		}
		subtitle := fmt.Sprintf("%s · %s", status, server.Transport)
		if strings.TrimSpace(detail) != "" {
			subtitle += " · " + detail
		}
		items = append(items, tui.DialogItem{
			Label:    server.Name,
			Subtitle: subtitle,
			Current:  !server.Disabled,
			Value:    server.Name,
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
	s, err := loadSanitizedSession(context.Background(), application, sessionName, *cfg)
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

func handleSkillToggleSelection(cfg *config.Config, application *app.App, skillName, persistencePath string) tui.SelectResult {
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return tui.SelectResult{Message: "Skill name is required."}
	}
	if application == nil {
		return tui.SelectResult{Message: "Application is not available."}
	}
	next := *cfg
	currentlyEnabled := false
	if application != nil && application.SkillRegistry != nil {
		_, currentlyEnabled = application.SkillRegistry.Find(skillName)
	}
	if currentlyEnabled {
		next.Skills.Disabled = appendUnique(next.Skills.Disabled, skillName)
		next.Skills.Enabled = removeString(next.Skills.Enabled, skillName)
	} else {
		registry := skill.LoadFromDirs(next.Skills.Paths...)
		if _, ok := registry.Find(skillName); !ok {
			return tui.SelectResult{Message: fmt.Sprintf("Skill %q is not available in configured skill directories.", skillName)}
		}
		next.Skills.Disabled = removeString(next.Skills.Disabled, skillName)
		if len(next.Skills.Enabled) > 0 {
			next.Skills.Enabled = appendUnique(next.Skills.Enabled, skillName)
		}
	}
	if err := next.Normalize(); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not update skill %q: %v", skillName, err)}
	}
	if err := application.ReloadFeatures(next, nil); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not reload skills after updating %q: %v", skillName, err)}
	}
	*cfg = next
	message := fmt.Sprintf("Disabled skill %s.", skillName)
	if !currentlyEnabled {
		message = fmt.Sprintf("Enabled skill %s.", skillName)
	}
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"skills": map[string]any{"enabled": next.Skills.Enabled, "disabled": next.Skills.Disabled}}); err != nil {
		message += fmt.Sprintf("\nWarning: could not persist skill toggle: %v", err)
	}
	return tui.SelectResult{Message: message}
}

func handleMCPToggleSelection(cfg *config.Config, application *app.App, serverName, persistencePath string) tui.SelectResult {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return tui.SelectResult{Message: "MCP server name is required."}
	}
	if application == nil {
		return tui.SelectResult{Message: "Application is not available."}
	}
	next := *cfg
	servers := configCloneMCPServers(next.MCP.Servers)
	index := -1
	for i := range servers {
		if servers[i].Name == serverName {
			index = i
			break
		}
	}
	if index < 0 {
		return tui.SelectResult{Message: fmt.Sprintf("MCP server %q not found.", serverName)}
	}
	servers[index].Disabled = !servers[index].Disabled
	if !servers[index].Disabled {
		if err := application.CheckMCPServer(servers[index], nil); err != nil {
			return tui.SelectResult{Message: fmt.Sprintf("Could not enable MCP server %q: %v", serverName, err)}
		}
	}
	next.MCP.Servers = servers
	next.MCPServers = configCloneMCPServers(servers)
	if err := next.Normalize(); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not update MCP server %q: %v", serverName, err)}
	}
	if err := application.ReloadFeatures(next, nil); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not reload MCP after updating %q: %v", serverName, err)}
	}
	*cfg = next
	message := fmt.Sprintf("Enabled MCP server %s.", serverName)
	if servers[index].Disabled {
		message = fmt.Sprintf("Disabled MCP server %s.", serverName)
	}
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{"mcp": map[string]any{"servers": next.MCP.Servers}}); err != nil {
		message += fmt.Sprintf("\nWarning: could not persist MCP toggle: %v", err)
	}
	return tui.SelectResult{Message: message}
}

func chatMessagesFromSession(s *session.Session) []tui.ChatMessage {
	if s == nil {
		return nil
	}
	messages := []tui.ChatMessage{{Role: "system", Content: fmt.Sprintf("Loaded session: %s", s.Metadata.ID), TimeStamp: time.Now()}}
	for index, msg := range s.Messages {
		timestamp := s.MessageTimestamp(index)
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
				messages = append(messages, tui.ChatMessage{Role: displayRole, Content: text, TimeStamp: timestamp})
			case block.OfToolUse != nil:
				input := ""
				if raw, err := json.Marshal(block.OfToolUse.Input); err == nil {
					input = string(raw)
				}
				messages = append(messages, tui.ChatMessage{
					Role:      "tool",
					ToolName:  block.OfToolUse.Name,
					Content:   input,
					TimeStamp: timestamp,
				})
			case block.OfToolResult != nil:
				text := toolResultText(block.OfToolResult)
				messages = append(messages, tui.ChatMessage{
					Role:      "tool-done",
					ToolName:  "Result",
					Content:   text,
					Collapsed: true,
					TimeStamp: timestamp,
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

func handleNewSessionCommand(cfg *config.Config, application *app.App, args, persistencePath string, crossSessionMemory bool) tui.SelectResult {
	if application == nil || application.Sessions == nil {
		return tui.SelectResult{Message: "Sessions are not enabled."}
	}
	name := strings.TrimSpace(args)
	if name == "" {
		name = "session-" + time.Now().Format("20060102-150405")
	}
	name = sanitizeSessionName(name)
	if name == "" {
		return tui.SelectResult{Message: "Session name is required."}
	}
	s, err := loadSanitizedSession(context.Background(), application, name, *cfg)
	if err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not create session %q: %v", name, err)}
	}
	if s == nil {
		s = session.NewSession(sessionID(name), cfg.WorkDir, cfg.Model)
	}
	s.Metadata.Title = name
	s.Metadata.CrossSessionMemory = &crossSessionMemory
	s.Metadata.MemoryBootstrapPending = crossSessionMemory
	if err := application.Sessions.Save(context.Background(), s); err != nil {
		return tui.SelectResult{Message: fmt.Sprintf("Could not save session %q: %v", name, err)}
	}
	cfg.Session.DefaultSession = name
	application.Config.Session.DefaultSession = name
	messages := []tui.ChatMessage{{Role: "system", Content: fmt.Sprintf("Started new session: %s", name), TimeStamp: time.Now()}}
	if len(s.Messages) == 0 {
		messages = append(messages, tui.ChatMessage{Role: "welcome", Content: "New session created.", TimeStamp: time.Now()})
	} else {
		messages = append(messages, chatMessagesFromSession(s)...)
	}
	message := persistDefaultSessionMessage(fmt.Sprintf("Started new session: %s (cross-session memory: %v)", name, crossSessionMemory), persistencePath, name)
	return tui.SelectResult{
		Message:         message,
		Messages:        messages,
		ReplaceMessages: true,
	}
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

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func removeString(values []string, target string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func appSkillNames(application *app.App) []string {
	if application == nil || application.SkillRegistry == nil {
		return nil
	}
	defs := application.SkillRegistry.All()
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}

func loadSanitizedSession(ctx context.Context, application *app.App, sessionName string, cfg config.Config) (*session.Session, error) {
	if application == nil || application.Sessions == nil {
		return nil, nil
	}
	s, err := application.Sessions.LoadOrCreate(ctx, sessionID(sessionName), cfg.WorkDir, cfg.Model)
	if err != nil {
		return nil, err
	}
	if application.SanitizeLoadedSession(s) {
		if err := application.Sessions.Save(context.WithoutCancel(ctx), s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func appcoreSanitizedSummary(application *app.App, s *session.Session) string {
	if s == nil {
		return ""
	}
	if application != nil {
		application.SanitizeLoadedSession(s)
	}
	return strings.TrimSpace(s.Summary)
}

func usageFromSession(cfg config.Config, application *app.App, s *session.Session) engine.Usage {
	if s == nil {
		return engine.Usage{MaxContextTokens: cfg.MaxContextTokens}
	}
	workDir := s.Metadata.WorkDir
	if strings.TrimSpace(workDir) == "" {
		workDir = cfg.WorkDir
	}
	builder := engine.ContextBuilder{SystemPrompt: cfg.SystemPrompt, SkillNames: appSkillNames(application)}
	var tools []tool.Tool
	if application != nil && application.Tools != nil {
		tools = application.Tools.All()
	}
	estimated := builder.EstimateContextTokens(engine.BuildRequest{
		Model:          cfg.Model,
		MaxTokens:      cfg.MaxTokens,
		WorkDir:        workDir,
		Messages:       s.CopyMessages(),
		Tools:          tools,
		Thinking:       cfg.Thinking,
		ThinkingText:   cfg.ThinkingText,
		Effort:         cfg.Effort,
		Stream:         cfg.Stream,
		SessionSummary: appcoreSanitizedSummary(application, s),
	})
	return engine.Usage{EstimatedContextTokens: estimated, MaxContextTokens: cfg.MaxContextTokens}
}

func configCloneMCPServers(servers []config.MCPServerConfig) []config.MCPServerConfig {
	if len(servers) == 0 {
		return nil
	}
	out := make([]config.MCPServerConfig, len(servers))
	copy(out, servers)
	for i := range out {
		out[i].Args = append([]string(nil), out[i].Args...)
		if len(out[i].Headers) > 0 {
			headers := make(map[string]string, len(out[i].Headers))
			for k, v := range out[i].Headers {
				headers[k] = v
			}
			out[i].Headers = headers
		}
		if len(out[i].Env) > 0 {
			env := make(map[string]string, len(out[i].Env))
			for k, v := range out[i].Env {
				env[k] = v
			}
			out[i].Env = env
		}
	}
	return out
}

func sessionID(value string) session.SessionID {
	return session.SessionID(value)
}
