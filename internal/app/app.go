package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	MemoryManager *memory.Manager
	SkillRegistry *skill.Registry
	MCPRegistry   *mcp.Registry
	mcpFactory    mcp.ClientFactory

	onTextDelta     func(string)
	onThinkingDelta func(string)
	onToolStart     func(name string, input json.RawMessage)
	onToolDone      func(name string, output string, isError bool)
	onUsage         func(engine.Usage)
	onStatus        func(string)
	onAskUser       func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)
}

type Option func(*options)

type options struct {
	mcpFactory      mcp.ClientFactory
	onTextDelta     func(string)
	onThinkingDelta func(string)
	onToolStart     func(name string, input json.RawMessage)
	onToolDone      func(name string, output string, isError bool)
	onUsage         func(engine.Usage)
	onStatus        func(string)
	onAskUser       func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)
}

func buildToolState(cfg config.Config, mcpFactory mcp.ClientFactory) (*tool.Registry, *skill.Registry, *mcp.Registry, error) {
	registry := tool.NewRegistry()
	registerBuiltins(registry)

	skillRegistry := loadSkills(cfg)
	if defs := skillRegistry.All(); len(defs) > 0 {
		registry.Register(tool.NewSkillTool(skillRegistry))
	}

	mcpRegistry := mcp.NewRegistry(cfg.MCP.Servers)
	if mcpFactory != nil {
		mcpRegistry.SetClientFactory(mcpFactory)
	}
	if err := mcpRegistry.Load(); err != nil {
		return nil, nil, nil, fmt.Errorf("load mcp registry: %w", err)
	}
	if mcpTools := mcpRegistry.Tools(); len(mcpTools) > 0 {
		registry.Register(mcpTools...)
	}
	return registry, skillRegistry, mcpRegistry, nil
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

func WithStatusCallback(onStatus func(string)) Option {
	return func(o *options) {
		o.onStatus = onStatus
	}
}

func WithAskUserCallback(onAskUser func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)) Option {
	return func(o *options) {
		o.onAskUser = onAskUser
	}
}

func New(cfg config.Config, opts ...Option) (*App, error) {
	var options options
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	registry, skillRegistry, mcpRegistry, err := buildToolState(cfg, options.mcpFactory)
	if err != nil {
		return nil, err
	}

	runtime := hook.NewRuntime(cfg.Hooks)
	permissions := permission.NewServiceWithConfig(cfg.Permissions)
	client := cpanthropic.NewClient(cpanthropic.Options{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
	})

	eng := engine.NewEngine(engineConfig(cfg, client, runtime, registry, permissions, options.onTextDelta, options.onThinkingDelta, options.onToolStart, options.onToolDone, options.onUsage, options.onAskUser))
	coordinator := agent.NewCoordinator(eng)
	registry.Register(tool.NewTaskTool(coordinator))

	var sessions *session.Manager
	if cfg.Session.Enabled && cfg.Session.Persist {
		sessions = session.NewManager(session.NewFileStore(cfg.Session.Dir), session.SessionID(cfg.Session.DefaultSession))
	}
	var memoryStore *memory.FileStore
	var memoryManager *memory.Manager
	if cfg.Memory.Enabled {
		memoryStore = memory.NewFileStore(cfg.Memory.Dir)
		memoryManager = memory.NewManagerWithExtractor(
			memoryStore,
			memory.DefaultGate{},
			memory.AnthropicJudge{Client: client, Model: cfg.Model},
			memory.AnthropicExtractor{Client: client, Model: cfg.Model},
		).WithLifecycle(memory.Lifecycle{Config: memory.LifecycleConfig{
			M1TTL:                    time.Duration(cfg.Memory.TierM1TTLHours) * time.Hour,
			M2TTL:                    time.Duration(cfg.Memory.TierM2TTLHours) * time.Hour,
			PromotionAccessThreshold: cfg.Memory.PromotionAccessThreshold,
			PromotionConfidence:      cfg.Memory.PromotionConfidence,
		}}).WithRetrievalBudget(cfg.Memory.RetrievalM2Limit, cfg.Memory.RetrievalM3Limit, cfg.Memory.RetrievalM4Limit, cfg.Memory.RetrievalM5Limit)
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
		MemoryManager:   memoryManager,
		SkillRegistry:   skillRegistry,
		MCPRegistry:     mcpRegistry,
		mcpFactory:      options.mcpFactory,
		onTextDelta:     options.onTextDelta,
		onThinkingDelta: options.onThinkingDelta,
		onToolStart:     options.onToolStart,
		onToolDone:      options.onToolDone,
		onUsage:         options.onUsage,
		onStatus:        options.onStatus,
		onAskUser:       options.onAskUser,
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
	if a.MemoryStore != nil && cfg.Memory.Enabled {
		a.MemoryManager = memory.NewManagerWithExtractor(
			a.MemoryStore,
			memory.DefaultGate{},
			memory.AnthropicJudge{Client: client, Model: cfg.Model},
			memory.AnthropicExtractor{Client: client, Model: cfg.Model},
		).WithLifecycle(memory.Lifecycle{Config: memory.LifecycleConfig{
			M1TTL:                    time.Duration(cfg.Memory.TierM1TTLHours) * time.Hour,
			M2TTL:                    time.Duration(cfg.Memory.TierM2TTLHours) * time.Hour,
			PromotionAccessThreshold: cfg.Memory.PromotionAccessThreshold,
			PromotionConfidence:      cfg.Memory.PromotionConfidence,
		}}).WithRetrievalBudget(cfg.Memory.RetrievalM2Limit, cfg.Memory.RetrievalM3Limit, cfg.Memory.RetrievalM4Limit, cfg.Memory.RetrievalM5Limit)
	}
	a.Engine.UpdateConfig(engineConfig(cfg, client, a.Hooks, a.Tools, a.Permissions, a.onTextDelta, a.onThinkingDelta, a.onToolStart, a.onToolDone, a.onUsage, a.onAskUser))
	return nil
}

func (a *App) ReloadFeatures(cfg config.Config, mcpFactory mcp.ClientFactory) error {
	if a == nil {
		return fmt.Errorf("app is nil")
	}
	if mcpFactory == nil {
		mcpFactory = a.mcpFactory
	}
	registry, skillRegistry, mcpRegistry, err := buildToolState(cfg, mcpFactory)
	if err != nil {
		return err
	}
	if a.MCPRegistry != nil {
		_ = a.MCPRegistry.Close()
	}
	a.Config = cfg
	a.Tools = registry
	a.SkillRegistry = skillRegistry
	a.MCPRegistry = mcpRegistry
	if cfg.Memory.Enabled {
		if a.MemoryStore == nil {
			a.MemoryStore = memory.NewFileStore(cfg.Memory.Dir)
		}
		a.MemoryManager = memory.NewManagerWithExtractor(
			a.MemoryStore,
			memory.DefaultGate{},
			memory.AnthropicJudge{Client: a.Client, Model: cfg.Model},
			memory.AnthropicExtractor{Client: a.Client, Model: cfg.Model},
		).WithLifecycle(memory.Lifecycle{Config: memory.LifecycleConfig{
			M1TTL:                    time.Duration(cfg.Memory.TierM1TTLHours) * time.Hour,
			M2TTL:                    time.Duration(cfg.Memory.TierM2TTLHours) * time.Hour,
			PromotionAccessThreshold: cfg.Memory.PromotionAccessThreshold,
			PromotionConfidence:      cfg.Memory.PromotionConfidence,
		}}).WithRetrievalBudget(cfg.Memory.RetrievalM2Limit, cfg.Memory.RetrievalM3Limit, cfg.Memory.RetrievalM4Limit, cfg.Memory.RetrievalM5Limit)
	} else {
		a.MemoryManager = nil
	}
	a.Engine.UpdateConfig(engineConfig(cfg, a.Client, a.Hooks, a.Tools, a.Permissions, a.onTextDelta, a.onThinkingDelta, a.onToolStart, a.onToolDone, a.onUsage, a.onAskUser))
	return nil
}

func (a *App) CheckMCPServer(server config.MCPServerConfig, mcpFactory mcp.ClientFactory) error {
	if mcpFactory == nil && a != nil {
		mcpFactory = a.mcpFactory
	}
	registry := mcp.NewRegistry([]config.MCPServerConfig{server})
	if mcpFactory != nil {
		registry.SetClientFactory(mcpFactory)
	}
	if err := registry.Load(); err != nil {
		return err
	}
	return registry.Close()
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
	if a.Config.Memory.Enabled && a.shouldCompact(ctx, current) {
		_, _ = a.compactSession(ctx, current)
	}
	memoryContext, err := a.retrieveMemoryContext(ctx, prompt, current)
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
	}
	saveCtx := context.WithoutCancel(ctx)
	if err := a.Sessions.Save(saveCtx, current); err != nil {
		return result.AgentResult, fmt.Errorf("save session: %w", err)
	}
	return result.AgentResult, nil
}

func (a *App) retrieveMemoryContext(ctx context.Context, prompt string, current *session.Session) ([]engine.ContextItem, error) {
	if a == nil || a.MemoryManager == nil || !a.Config.Memory.Enabled {
		return nil, nil
	}
	allowCrossSession := sessionAllowsCrossSessionMemory(current)
	currentSessionID := ""
	if current != nil {
		currentSessionID = string(current.Metadata.ID)
	}
	selected, err := a.MemoryManager.Retrieve(ctx, prompt, currentSessionID, allowCrossSession, a.Config.Memory.RetrievalLimit)
	if err != nil {
		return nil, err
	}
	out := make([]engine.ContextItem, 0, len(selected))
	for _, item := range selected {
		content := strings.TrimSpace(item.Text)
		if content == "" {
			continue
		}
		if len([]rune(content)) > 1000 {
			content = string([]rune(content)[:1000]) + "..."
		}
		title := strings.Join(item.Tags, ", ")
		if title == "" {
			title = "Memory"
		}
		if item.Kind != "" {
			title += " · " + string(item.Kind)
		}
		if item.SourceSessionID != "" {
			title += " · session " + item.SourceSessionID
		}
		out = append(out, engine.ContextItem{
			Title:      title,
			Content:    content,
			Source:     item.ID,
			Importance: item.Importance,
		})
		_ = a.MemoryStore.Touch(ctx, item)
	}
	return out, nil
}

func (a *App) rememberExplicitMemory(ctx context.Context, prompt, sessionID string) {
	if a == nil || a.MemoryManager == nil || !a.Config.Memory.Enabled {
		return
	}
	text, ok := memory.ExplicitMemoryFromPrompt(prompt)
	if !ok {
		return
	}
	existingSummary := ""
	if a.Sessions != nil {
		if current, err := a.Sessions.LoadOrCreate(ctx, session.SessionID(sessionID), a.Config.WorkDir, a.Config.Model); err == nil && current != nil {
			existingSummary = current.Summary
		}
	}
	_, _, _ = a.MemoryManager.RememberExplicit(ctx, text, sessionID, a.Config.WorkDir, existingSummary)
}

func (a *App) shouldCompact(ctx context.Context, current *session.Session) bool {
	if current == nil || len(current.Messages) == 0 {
		return false
	}
	trigger := a.compactionTriggerTokens()
	estimated := a.estimateSessionContextTokens(ctx, current)
	return trigger > 0 && estimated >= trigger
}

func (a *App) compactionTriggerTokens() int {
	if a == nil {
		return 0
	}
	trigger := a.Config.Memory.SummaryThresholdTokens
	if a.Config.MaxContextTokens > 0 {
		pct := a.Config.Memory.CompactionTriggerPercent
		if pct <= 0 {
			pct = 85
		}
		percentThreshold := int(a.Config.MaxContextTokens) * pct / 100
		if percentThreshold > 0 {
			trigger = percentThreshold
		}
	}
	return trigger
}

func (a *App) estimateSessionContextTokens(ctx context.Context, current *session.Session) int {
	if a == nil || current == nil {
		return 0
	}
	memoryContext := []engine.ContextItem(nil)
	if a.MemoryManager != nil {
		allowCrossSession := sessionAllowsCrossSessionMemory(current)
		query := strings.TrimSpace(current.Summary)
		if query == "" {
			query = session.Transcript(current.CopyMessages())
		}
		items, err := a.MemoryManager.Retrieve(ctx, query, string(current.Metadata.ID), allowCrossSession, a.Config.Memory.RetrievalLimit)
		if err == nil {
			for _, item := range items {
				content := strings.TrimSpace(item.Text)
				if content == "" {
					continue
				}
				memoryContext = append(memoryContext, engine.ContextItem{Title: strings.Join(item.Tags, ", "), Content: content, Source: item.ID, Importance: item.Importance})
			}
		}
	}
	builder := engine.ContextBuilder{SystemPrompt: a.Config.SystemPrompt, SkillNames: skillNames(a.SkillRegistry)}
	tools := []tool.Tool(nil)
	if a.Tools != nil {
		tools = a.Tools.All()
	}
	return int(builder.EstimateContextTokens(engine.BuildRequest{
		Model:          a.Config.Model,
		MaxTokens:      a.Config.MaxTokens,
		WorkDir:        current.Metadata.WorkDir,
		Messages:       current.CopyMessages(),
		Tools:          tools,
		Thinking:       a.Config.Thinking,
		ThinkingText:   a.Config.ThinkingText,
		Effort:         a.Config.Effort,
		Stream:         a.Config.Stream,
		SessionSummary: current.Summary,
		MemoryContext:  memoryContext,
	}))
}

func (a *App) CompactSession(ctx context.Context, sessionID, workDir string) (*session.Session, bool, error) {
	if a == nil {
		return nil, false, fmt.Errorf("app is nil")
	}
	if a.Sessions == nil {
		return nil, false, fmt.Errorf("sessions are not enabled")
	}
	if !a.Config.Memory.Enabled {
		return nil, false, fmt.Errorf("memory is not enabled")
	}
	if sessionID == "" {
		sessionID = a.Config.Session.DefaultSession
	}
	if sessionID == "" {
		sessionID = "main"
	}
	if workDir == "" {
		workDir = a.Config.WorkDir
	}
	current, err := a.Sessions.LoadOrCreate(ctx, session.SessionID(sessionID), workDir, a.Config.Model)
	if err != nil {
		return nil, false, fmt.Errorf("load session: %w", err)
	}
	changed, err := a.compactSession(ctx, current)
	if err != nil {
		return current, false, err
	}
	if changed {
		if err := a.Sessions.Save(context.WithoutCancel(ctx), current); err != nil {
			return current, true, fmt.Errorf("save compacted session: %w", err)
		}
	}
	return current, changed, nil
}

func (a *App) compactSession(ctx context.Context, current *session.Session) (bool, error) {
	if a == nil || current == nil || !a.Config.Memory.Enabled {
		return false, nil
	}
	if a.onStatus != nil {
		a.onStatus("Compacting...")
		defer a.onStatus("Ready")
	}
	trigger := a.compactionTriggerTokens()
	estimated := a.estimateSessionContextTokens(ctx, current)
	a.recordCompactEvent("compact_started", map[string]any{
		"session_id":       string(current.Metadata.ID),
		"estimated_tokens": estimated,
		"trigger_tokens":   trigger,
		"messages_before":  len(current.Messages),
		"summary_runes":    len([]rune(current.Summary)),
	})
	target := trigger * a.Config.Memory.CompactionTargetPercent / 100
	if target <= 0 {
		target = trigger * 50 / 100
	}
	result, err := session.Compact(ctx, current.Summary, current.CopyMessages(), nil, session.CompactOptions{
		MaxRecentTurns:         a.Config.Memory.MaxRecentTurns,
		SummaryThresholdTokens: trigger,
		TargetTokens:           target,
		EstimatedTokens:        estimated,
	})
	if err != nil {
		a.recordCompactEvent("compact_failed", map[string]any{
			"session_id": string(current.Metadata.ID),
			"error":      err.Error(),
		})
		return false, err
	}
	if !result.Changed {
		a.recordCompactEvent("compact_skipped", map[string]any{"session_id": string(current.Metadata.ID)})
		return false, nil
	}
	previousSummary := current.Summary
	beforeMessages := len(current.Messages)
	current.Summary = ""
	current.ReplaceMessages(result.Messages)
	retainedTokens := a.estimateSessionContextTokens(ctx, current)
	a.recordCompactEvent("compact_succeeded", map[string]any{
		"session_id":          string(current.Metadata.ID),
		"messages_before":     beforeMessages,
		"messages_after":      len(result.Messages),
		"summary_runes":       len([]rune(result.Summary)),
		"original_runes":      len([]rune(result.OriginalTranscript)),
		"compacted_runes":     len([]rune(result.CompactedTranscript)),
		"retained_runes":      len([]rune(result.RetainedTranscript)),
		"discarded_runes":     len([]rune(result.DiscardedTranscript)),
		"retained_tokens":     retainedTokens,
		"used_local_fallback": false,
	})
	if a.MemoryManager != nil && strings.TrimSpace(result.CompactedTranscript) != "" {
		items, memErr := a.MemoryManager.RememberExtracted(ctx, memory.ExtractionInput{
			SourceSessionID:     string(current.Metadata.ID),
			WorkDir:             current.Metadata.WorkDir,
			PreviousSummary:     previousSummary,
			NewSummary:          result.Summary,
			Transcript:          result.CompactedTranscript,
			OriginalTranscript:  result.OriginalTranscript,
			CompactedTranscript: result.CompactedTranscript,
			RetainedTranscript:  result.RetainedTranscript,
			DiscardedTranscript: result.DiscardedTranscript,
		})
		if memErr != nil {
			a.recordCompactEvent("memory_extract_failed", map[string]any{
				"session_id": string(current.Metadata.ID),
				"error":      memErr.Error(),
			})
		} else {
			a.recordCompactEvent("memory_extract_succeeded", map[string]any{
				"session_id": string(current.Metadata.ID),
				"items":      len(items),
			})
		}
	}
	return true, nil
}

func sessionAllowsCrossSessionMemory(current *session.Session) bool {
	if current == nil || current.Metadata.CrossSessionMemory == nil {
		return false
	}
	return *current.Metadata.CrossSessionMemory
}

func (a *App) recordCompactEvent(kind string, fields map[string]any) {
	if a == nil {
		return
	}
	path := filepath.Join(config.UserStateDir(), projectStateSubdir(a.Config.WorkDir), "compact.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	entry := map[string]any{
		"time": time.Now().Format(time.RFC3339),
		"kind": kind,
	}
	for key, value := range fields {
		entry[key] = value
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(data, '\n'))
}

func projectStateSubdir(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range filepath.ToSlash(workDir) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func engineConfig(cfg config.Config, client *cpanthropic.Client, runtime *hook.Runtime, registry *tool.Registry, permissions *permission.Service, onTextDelta, onThinkingDelta func(string), onToolStart func(name string, input json.RawMessage), onToolDone func(name string, output string, isError bool), onUsage func(engine.Usage), onAskUser func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)) engine.Config {
	skillRegistry := loadSkills(cfg)
	return engine.Config{
		Client:           client,
		Hooks:            runtime,
		Tools:            registry,
		Permissions:      permissions,
		ModelName:        cfg.Model,
		MaxContextTokens: cfg.MaxContextTokens,
		MaxTokens:        cfg.MaxTokens,
		SystemPrompt:     cfg.SystemPrompt,
		SkillNames:       skillNames(skillRegistry),
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
		OnAskUser:        onAskUser,
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

func skillNames(registry *skill.Registry) []string {
	if registry == nil {
		return nil
	}
	defs := registry.All()
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}
