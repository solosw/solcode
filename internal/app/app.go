package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/codeplus-agent/internal/agent"
	cpanthropic "github.com/solosw/codeplus-agent/internal/anthropic"
	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/engine"
	"github.com/solosw/codeplus-agent/internal/hook"
	"github.com/solosw/codeplus-agent/internal/lsp"
	"github.com/solosw/codeplus-agent/internal/mcp"
	"github.com/solosw/codeplus-agent/internal/memory"
	"github.com/solosw/codeplus-agent/internal/permission"
	"github.com/solosw/codeplus-agent/internal/session"
	"github.com/solosw/codeplus-agent/internal/skill"
	"github.com/solosw/codeplus-agent/internal/tool"
)

type App struct {
	Config        config.Config
	Client        *cpanthropic.Client
	Tools         *tool.Registry
	Hooks         *hook.Runtime
	Permissions   *permission.Service
	Engine        *engine.Engine
	Coordinator   *agent.Coordinator
	Sessions      *session.Manager
	MemoryStore   *memory.FileStore
	SkillRegistry *skill.Registry
	MCPRegistry   *mcp.Registry

	onTextDelta     func(string)
	onThinkingDelta func(string)
	onToolStart     func(name string, input json.RawMessage)
	onToolDone      func(name string, output string, isError bool)
	onUsage         func(engine.Usage)
}

type Option func(*options)

type options struct {
	mcpFactory      mcp.ClientFactory
	onTextDelta     func(string)
	onThinkingDelta func(string)
	onToolStart     func(name string, input json.RawMessage)
	onToolDone      func(name string, output string, isError bool)
	onUsage         func(engine.Usage)
}

func WithMCPClientFactory(factory mcp.ClientFactory) Option {
	return func(o *options) {
		o.mcpFactory = factory
	}
}

func WithStreamCallbacks(onTextDelta, onThinkingDelta func(string)) Option {
	return func(o *options) {
		o.onTextDelta = onTextDelta
		o.onThinkingDelta = onThinkingDelta
	}
}

func WithToolCallbacks(onToolStart func(name string, input json.RawMessage), onToolDone func(name string, output string, isError bool)) Option {
	return func(o *options) {
		o.onToolStart = onToolStart
		o.onToolDone = onToolDone
	}
}

func WithUsageCallback(onUsage func(engine.Usage)) Option {
	return func(o *options) {
		o.onUsage = onUsage
	}
}

func New(cfg config.Config, opts ...Option) (*App, error) {
	var options options
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	registry := tool.NewRegistry()
	registerBuiltins(registry)

	skillRegistry := loadSkills(cfg)
	if defs := skillRegistry.All(); len(defs) > 0 {
		registry.Register(tool.NewSkillTool(skillRegistry))
	}

	mcpRegistry := mcp.NewRegistry(cfg.MCP.Servers)
	if options.mcpFactory != nil {
		mcpRegistry.SetClientFactory(options.mcpFactory)
	}
	if err := mcpRegistry.Load(); err != nil {
		return nil, fmt.Errorf("load mcp registry: %w", err)
	}
	if mcpTools := mcpRegistry.Tools(); len(mcpTools) > 0 {
		registry.Register(mcpTools...)
	}

	runtime := hook.NewRuntime(cfg.Hooks)
	permissions := permission.NewServiceWithConfig(cfg.Permissions)
	client := cpanthropic.NewClient(cpanthropic.Options{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
	})

	eng := engine.NewEngine(engineConfig(cfg, client, runtime, registry, permissions, options.onTextDelta, options.onThinkingDelta, options.onToolStart, options.onToolDone, options.onUsage))
	coordinator := agent.NewCoordinator(eng)
	registry.Register(tool.NewTaskTool(coordinator))

	var sessions *session.Manager
	if cfg.Session.Enabled && cfg.Session.Persist {
		sessions = session.NewManager(session.NewFileStore(cfg.Session.Dir), session.SessionID(cfg.Session.DefaultSession))
	}
	var memoryStore *memory.FileStore
	if cfg.Memory.Enabled {
		memoryStore = memory.NewFileStore(cfg.Memory.Dir)
	}

	return &App{
		Config:          cfg,
		Client:          client,
		Tools:           registry,
		Hooks:           runtime,
		Permissions:     permissions,
		Engine:          eng,
		Coordinator:     coordinator,
		Sessions:        sessions,
		MemoryStore:     memoryStore,
		SkillRegistry:   skillRegistry,
		MCPRegistry:     mcpRegistry,
		onTextDelta:     options.onTextDelta,
		onThinkingDelta: options.onThinkingDelta,
		onToolStart:     options.onToolStart,
		onToolDone:      options.onToolDone,
		onUsage:         options.onUsage,
	}, nil
}

func (a *App) Close() error {
	if a == nil || a.MCPRegistry == nil {
		return nil
	}
	return a.MCPRegistry.Close()
}

func (a *App) SwitchModel(cfg config.Config) error {
	if a == nil {
		return fmt.Errorf("app is nil")
	}
	client := a.Client
	if cfg.APIKey != a.Config.APIKey || cfg.BaseURL != a.Config.BaseURL {
		client = cpanthropic.NewClient(cpanthropic.Options{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL})
	}
	a.Config = cfg
	a.Client = client
	a.Engine.UpdateConfig(engineConfig(cfg, client, a.Hooks, a.Tools, a.Permissions, a.onTextDelta, a.onThinkingDelta, a.onToolStart, a.onToolDone, a.onUsage))
	return nil
}

func (a *App) RunPrompt(ctx context.Context, prompt, workDir string, maxTurns int) (agent.AgentResult, error) {
	if a == nil {
		return agent.AgentResult{}, fmt.Errorf("app is nil")
	}
	if prompt == "" {
		return agent.AgentResult{}, fmt.Errorf("prompt is required")
	}
	if workDir == "" {
		workDir = a.Config.WorkDir
	}
	if maxTurns <= 0 {
		maxTurns = a.Config.MaxTurns
	}
	cfg := agent.AgentConfig{
		ID:           agent.AgentID("main"),
		Role:         agent.AgentRoleMain,
		WorkDir:      workDir,
		Prompt:       prompt,
		AllowedTools: []string{},
		MaxTurns:     maxTurns,
	}
	return a.Engine.Run(ctx, cfg), nil
}

func (a *App) RunPromptWithSession(ctx context.Context, sessionID, prompt, workDir string, maxTurns int) (agent.AgentResult, error) {
	if a == nil {
		return agent.AgentResult{}, fmt.Errorf("app is nil")
	}
	if prompt == "" {
		return agent.AgentResult{}, fmt.Errorf("prompt is required")
	}
	if workDir == "" {
		workDir = a.Config.WorkDir
	}
	if maxTurns <= 0 {
		maxTurns = a.Config.MaxTurns
	}
	if sessionID == "" {
		sessionID = a.Config.Session.DefaultSession
	}
	if sessionID == "" {
		sessionID = "main"
	}
	if a.Sessions == nil {
		return a.RunPrompt(ctx, prompt, workDir, maxTurns)
	}
	current, err := a.Sessions.LoadOrCreate(ctx, session.SessionID(sessionID), workDir, a.Config.Model)
	if err != nil {
		return agent.AgentResult{}, fmt.Errorf("load session: %w", err)
	}
	memoryContext, err := a.retrieveMemoryContext(ctx, prompt)
	if err != nil {
		current.Append(sdk.NewUserMessage(sdk.NewTextBlock(prompt)))
		saveCtx := context.WithoutCancel(ctx)
		if saveErr := a.Sessions.Save(saveCtx, current); saveErr != nil {
			return agent.AgentResult{}, fmt.Errorf("save session after memory error: %w", saveErr)
		}
		return agent.AgentResult{}, fmt.Errorf("retrieve memory: %w", err)
	}
	cfg := agent.AgentConfig{
		ID:           agent.AgentID(sessionID),
		Role:         agent.AgentRoleMain,
		WorkDir:      workDir,
		Prompt:       prompt,
		AllowedTools: []string{},
		MaxTurns:     maxTurns,
	}
	result := a.Engine.RunWithHistory(ctx, engine.RunRequest{
		AgentConfig:    cfg,
		SessionID:      sessionID,
		Messages:       current.CopyMessages(),
		SessionSummary: current.Summary,
		MemoryContext:  memoryContext,
	})
	current.Metadata.WorkDir = workDir
	current.Metadata.Model = a.Config.Model
	if len(result.Messages) > 0 {
		current.ReplaceMessages(result.Messages)
	}
	if result.AgentResult.Error == "" && a.Config.Memory.Enabled {
		a.rememberExplicitMemory(ctx, prompt, sessionID)
		a.compactSession(ctx, current)
	}
	saveCtx := context.WithoutCancel(ctx)
	if err := a.Sessions.Save(saveCtx, current); err != nil {
		return result.AgentResult, fmt.Errorf("save session: %w", err)
	}
	return result.AgentResult, nil
}

func (a *App) retrieveMemoryContext(ctx context.Context, prompt string) ([]engine.ContextItem, error) {
	if a == nil || a.MemoryStore == nil || !a.Config.Memory.Enabled {
		return nil, nil
	}
	items, err := a.MemoryStore.List(ctx)
	if err != nil {
		return nil, err
	}
	selected := memory.KeywordRetriever{Items: items}.Retrieve(prompt, a.Config.Memory.RetrievalLimit)
	out := make([]engine.ContextItem, 0, len(selected))
	for _, item := range selected {
		content := strings.TrimSpace(item.Text)
		if content == "" {
			continue
		}
		if len([]rune(content)) > 1000 {
			content = string([]rune(content)[:1000]) + "..."
		}
		out = append(out, engine.ContextItem{
			Title:      strings.Join(item.Tags, ", "),
			Content:    content,
			Source:     item.ID,
			Importance: item.Importance,
		})
		_ = a.MemoryStore.Touch(ctx, item)
	}
	return out, nil
}

func (a *App) rememberExplicitMemory(ctx context.Context, prompt, sessionID string) {
	if a == nil || a.MemoryStore == nil || !a.Config.Memory.Enabled {
		return
	}
	text, ok := memory.ExplicitMemoryFromPrompt(prompt)
	if !ok {
		return
	}
	_, _, _ = a.MemoryStore.Remember(ctx, text, sessionID)
}

func (a *App) compactSession(ctx context.Context, current *session.Session) {
	if a == nil || current == nil || !a.Config.Memory.Enabled {
		return
	}
	result, err := session.Compact(ctx, current.Summary, current.CopyMessages(), anthropicSummaryWriter{app: a}, session.CompactOptions{
		MaxRecentTurns:         a.Config.Memory.MaxRecentTurns,
		SummaryThresholdTokens: a.Config.Memory.SummaryThresholdTokens,
	})
	if err != nil || !result.Changed {
		return
	}
	current.Summary = result.Summary
	current.ReplaceMessages(result.Messages)
}

type anthropicSummaryWriter struct {
	app *App
}

func (w anthropicSummaryWriter) Summarize(ctx context.Context, previous string, newContent string) (string, error) {
	if w.app == nil || w.app.Client == nil {
		return "", fmt.Errorf("summary client is nil")
	}
	prompt := "Summarize the durable context from this older conversation segment for future turns. Keep decisions, user preferences, unresolved tasks, important file paths, and constraints. Exclude transient tool chatter and secrets."
	content := "Previous summary:\n" + strings.TrimSpace(previous) + "\n\nOlder conversation segment:\n" + strings.TrimSpace(newContent)
	message, err := w.app.Client.Create(ctx, cpanthropic.MessageRequest{
		Model:     w.app.Config.Model,
		MaxTokens: 1200,
		System:    prompt,
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(content))},
		Thinking:  false,
		Stream:    false,
	})
	if err != nil {
		return "", err
	}
	return cpanthropic.TextFromMessage(message), nil
}

func engineConfig(cfg config.Config, client *cpanthropic.Client, runtime *hook.Runtime, registry *tool.Registry, permissions *permission.Service, onTextDelta, onThinkingDelta func(string), onToolStart func(name string, input json.RawMessage), onToolDone func(name string, output string, isError bool), onUsage func(engine.Usage)) engine.Config {
	return engine.Config{
		Client:           client,
		Hooks:            runtime,
		Tools:            registry,
		Permissions:      permissions,
		ModelName:        cfg.Model,
		MaxContextTokens: cfg.MaxContextTokens,
		MaxTokens:        cfg.MaxTokens,
		SystemPrompt:     cfg.SystemPrompt,
		MaxTurns:         cfg.MaxTurns,
		Stream:           cfg.Stream,
		Thinking:         cfg.Thinking,
		ThinkingText:     cfg.ThinkingText,
		Effort:           cfg.Effort,
		TodoPath:         config.DefaultTodoPath(cfg.WorkDir),
		OnTextDelta:      onTextDelta,
		OnThinkingDelta:  onThinkingDelta,
		OnToolStart:      onToolStart,
		OnToolDone:       onToolDone,
		OnUsage:          onUsage,
	}
}

func registerBuiltins(registry *tool.Registry) {
	registry.Register(
		tool.NewAskUserTool(),
		tool.NewBashTool(),
		tool.NewDiffTool(),
		tool.NewEditTool(),
		tool.NewFetchTool(),
		tool.NewGlobTool(),
		tool.NewGrepTool(),
		tool.NewLsTool(),
		tool.NewLSPTool(lsp.NewManager(nil, nil)),
		tool.NewPatchTool(),
		tool.NewTodoWriteTool(),
		tool.NewViewTool(),
		tool.NewWebSearchTool(),
		tool.NewWriteTool(),
	)
}

func loadSkills(cfg config.Config) *skill.Registry {
	registry := skill.NewRegistry()
	for _, dir := range cfg.Skills.Paths {
		registryDir := dir
		if !filepath.IsAbs(registryDir) && cfg.WorkDir != "" {
			registryDir = filepath.Join(cfg.WorkDir, registryDir)
		}
		loaded := skill.LoadFromDirs(registryDir)
		for _, def := range loaded.All() {
			if len(cfg.Skills.Enabled) > 0 && !contains(cfg.Skills.Enabled, def.Name) {
				continue
			}
			if contains(cfg.Skills.Disabled, def.Name) {
				continue
			}
			registry.Add(def)
		}
	}
	return registry
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
