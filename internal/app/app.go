package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

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
		memoryModel := memoryModelName(cfg)
		memoryManager = memory.NewManagerWithExtractor(
			memoryStore,
			memory.DefaultGate{},
			memory.AnthropicJudge{Client: client, Model: memoryModel},
			memory.AnthropicExtractor{Client: client, Model: memoryModel},
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
		memoryModel := memoryModelName(cfg)
		a.MemoryManager = memory.NewManagerWithExtractor(
			a.MemoryStore,
			memory.DefaultGate{},
			memory.AnthropicJudge{Client: client, Model: memoryModel},
			memory.AnthropicExtractor{Client: client, Model: memoryModel},
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
	registry.Register(tool.NewTaskTool(a.Coordinator))
	if cfg.Memory.Enabled {
		if a.MemoryStore == nil {
			a.MemoryStore = memory.NewFileStore(cfg.Memory.Dir)
		}
		memoryModel := memoryModelName(cfg)
		a.MemoryManager = memory.NewManagerWithExtractor(
			a.MemoryStore,
			memory.DefaultGate{},
			memory.AnthropicJudge{Client: a.Client, Model: memoryModel},
			memory.AnthropicExtractor{Client: a.Client, Model: memoryModel},
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
	sessionStateChanged := a.SanitizeLoadedSession(current)
	if a.Config.Memory.Enabled {
		a.resetMemoryMaintenanceCycleIfBelowThreshold(ctx, current)
		memoryStateChanged := sessionStateChanged
		if a.shouldRefreshMemorySummary(ctx, current) {
			if _, err := a.extractSessionMemories(ctx, current, "background_summary"); err == nil {
				current.Metadata.MemorySummaryCompleted = true
				memoryStateChanged = true
			}
		}
		if a.shouldCompact(ctx, current) {
			if changed, err := a.compactSession(ctx, current); err == nil && changed {
				current.Metadata.MemoryCompactionCompleted = true
				current.Metadata.MemoryCompactionMessageCount = len(current.Messages)
				memoryStateChanged = true
			}
		}
		if memoryStateChanged {
			if err := a.Sessions.Save(context.WithoutCancel(ctx), current); err != nil {
				return agent.AgentResult{}, fmt.Errorf("save session after memory maintenance: %w", err)
			}
		}
	} else if sessionStateChanged {
		if err := a.Sessions.Save(context.WithoutCancel(ctx), current); err != nil {
			return agent.AgentResult{}, fmt.Errorf("save sanitized session: %w", err)
		}
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
		SessionSummary: sanitizeLoadedSessionSummary(current.Summary),
		MemoryContext:  memoryContext,
	})
	current.Metadata.WorkDir = workDir
	current.Metadata.Model = a.Config.Model
	if len(result.Messages) > 0 {
		current.ReplaceMessages(session.StripEphemeralContextMessages(result.Messages))
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
	selected, err := a.MemoryManager.Retrieve(ctx, prompt, currentSessionID, allowCrossSession, 0)
	if err != nil {
		return nil, err
	}
	return a.memoryContextFromItems(ctx, selected), nil
}

func (a *App) memoryContextFromItems(_ context.Context, items []memory.Item) []engine.ContextItem {
	out := make([]engine.ContextItem, 0, len(items))
	for _, item := range items {
		content := summarizeMemoryItemText(item)
		if content == "" {
			continue
		}
		title := strings.TrimSpace(string(item.Kind))
		if item.Scope != "" {
			if title != "" {
				title += "/"
			}
			title += string(item.Scope)
		}
		out = append(out, engine.ContextItem{
			Title:      title,
			Content:    content,
			Source:     string(item.Tier),
			Importance: item.Importance,
		})
	}
	return out
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

func (a *App) shouldRefreshMemorySummary(ctx context.Context, current *session.Session) bool {
	if current == nil || len(current.Messages) == 0 || current.Metadata.MemorySummaryCompleted {
		return false
	}
	trigger := a.memorySummaryTriggerTokens()
	estimated := a.estimateSessionContextTokens(ctx, current)
	return trigger > 0 && estimated >= trigger
}

func (a *App) resetMemoryMaintenanceCycleIfBelowThreshold(ctx context.Context, current *session.Session) {
	if current == nil || (!current.Metadata.MemorySummaryCompleted && !current.Metadata.MemoryCompactionCompleted) {
		return
	}
	estimated := a.estimateSessionContextTokens(ctx, current)
	if current.Metadata.MemorySummaryCompleted {
		trigger := a.memorySummaryTriggerTokens()
		if trigger > 0 && estimated < trigger {
			current.Metadata.MemorySummaryCompleted = false
		}
	}
	if current.Metadata.MemoryCompactionCompleted {
		trigger := a.compactionTriggerTokens()
		if trigger > 0 && estimated < trigger {
			current.Metadata.MemoryCompactionCompleted = false
		}
	}
}

func (a *App) memorySummaryTriggerTokens() int {
	if a == nil {
		return 0
	}
	trigger := a.Config.Memory.SummaryThresholdTokens
	if a.Config.MaxContextTokens > 0 {
		pct := a.Config.Memory.SummaryTriggerPercent
		if pct <= 0 {
			pct = 50
		}
		percentThreshold := int(a.Config.MaxContextTokens) * pct / 100
		if percentThreshold > 0 {
			trigger = percentThreshold
		}
	}
	return trigger
}

func (a *App) shouldCompact(ctx context.Context, current *session.Session) bool {
	if current == nil || len(current.Messages) == 0 {
		return false
	}
	if current.Metadata.MemoryCompactionCompleted && current.Metadata.MemoryCompactionMessageCount > 0 && len(current.Messages) <= current.Metadata.MemoryCompactionMessageCount {
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

func (a *App) memoryRetrievalTokenBudget() int {
	if a == nil {
		return 0
	}
	maxTokens := int(a.Config.MaxContextTokens)
	if maxTokens <= 0 {
		maxTokens = 200_000
	}
	percent := a.Config.Memory.RetrievalContextPercent
	if percent <= 0 {
		percent = 10
	}
	if percent > 10 {
		percent = 10
	}
	budget := maxTokens * percent / 100
	if budget < a.Config.Memory.RetrievalMinTokens {
		budget = a.Config.Memory.RetrievalMinTokens
	}
	if budget > a.Config.Memory.RetrievalMaxTokens {
		budget = a.Config.Memory.RetrievalMaxTokens
	}
	maxShare := maxTokens / 10
	if maxShare > 0 && budget > maxShare {
		budget = maxShare
	}
	return budget
}

func (a *App) estimateSessionContextTokens(ctx context.Context, current *session.Session) int {
	if a == nil || current == nil {
		return 0
	}
	messages := session.StripEphemeralContextMessages(current.CopyMessages())
	memoryContext := []engine.ContextItem(nil)
	if a.MemoryManager != nil {
		allowCrossSession := sessionAllowsCrossSessionMemory(current)
		query := strings.TrimSpace(sanitizeLoadedSessionSummary(current.Summary))
		if query == "" {
			query = session.Transcript(messages)
		}
		items, err := a.MemoryManager.Retrieve(ctx, query, string(current.Metadata.ID), allowCrossSession, 0)
		if err == nil {
			memoryContext = a.memoryContextFromItems(ctx, items)
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
		Messages:       messages,
		Tools:          tools,
		Thinking:       a.Config.Thinking,
		ThinkingText:   a.Config.ThinkingText,
		Effort:         a.Config.Effort,
		Stream:         a.Config.Stream,
		SessionSummary: sanitizeLoadedSessionSummary(current.Summary),
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
	preChanged := a.SanitizeLoadedSession(current)
	changed, err := a.compactSession(ctx, current)
	changed = changed || preChanged
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
	cleanedMessages := session.StripEphemeralContextMessages(current.CopyMessages())
	cleanedHistory := len(cleanedMessages) != len(current.Messages)
	if cleanedHistory {
		current.ReplaceMessages(cleanedMessages)
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
	target := 0
	if a.Config.MaxContextTokens > 0 && a.Config.Memory.CompactionTargetPercent > 0 {
		target = int(a.Config.MaxContextTokens) * a.Config.Memory.CompactionTargetPercent / 100
	}
	if target <= 0 {
		target = trigger * a.Config.Memory.CompactionTargetPercent / 100
	}
	if target <= 0 {
		target = trigger * 15 / 100
	}
	result, err := session.Compact(ctx, current.Summary, session.StripEphemeralContextMessages(current.CopyMessages()), nil, session.CompactOptions{
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
		if cleanedHistory || strings.TrimSpace(current.Summary) != "" {
			current.Summary = summarizeForContext(session.Transcript(cleanedMessages), "", nil)
			return true, nil
		}
		return false, nil
	}
	previousSummary := sanitizeLoadedSessionSummary(current.Summary)
	current.Summary = previousSummary
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
	current.Summary = summarizeForContext(result.CompactedTranscript, previousSummary, nil)
	if _, err := a.extractSessionMemories(ctx, current, "post_compaction"); err != nil {
		a.recordCompactEvent("memory_extract_failed", map[string]any{
			"session_id": string(current.Metadata.ID),
			"error":      err.Error(),
		})
	}
	_ = previousSummary
	return true, nil
}

func (a *App) extractSessionMemories(ctx context.Context, current *session.Session, reason string) ([]memory.Item, error) {
	if a == nil || current == nil || a.MemoryManager == nil || !a.Config.Memory.Enabled {
		return nil, nil
	}
	transcript := session.Transcript(session.StripEphemeralContextMessages(current.CopyMessages()))
	if strings.TrimSpace(transcript) == "" {
		return nil, nil
	}
	previousSummary := sanitizeLoadedSessionSummary(current.Summary)
	current.Summary = previousSummary
	estimated := a.estimateSessionContextTokens(ctx, current)
	items, err := a.MemoryManager.RememberExtracted(ctx, memory.ExtractionInput{
		SourceSessionID:     string(current.Metadata.ID),
		WorkDir:             current.Metadata.WorkDir,
		PreviousSummary:     previousSummary,
		NewSummary:          previousSummary,
		Transcript:          transcript,
		OriginalTranscript:  transcript,
		CompactedTranscript: transcript,
		RetainedTranscript:  transcript,
		DiscardedTranscript: "",
		TriggerReason:       reason,
		EstimatedTokens:     estimated,
	})
	if err != nil {
		return nil, err
	}
	current.Summary = summarizeForContext(transcript, previousSummary, items)
	a.recordCompactEvent("memory_extract_succeeded", map[string]any{
		"session_id":       string(current.Metadata.ID),
		"items":            len(items),
		"reason":           reason,
		"estimated_tokens": estimated,
	})
	return items, nil
}

func summarizeForContext(transcript, previous string, items []memory.Item) string {
	transcript = strings.TrimSpace(transcript)
	previous = strings.TrimSpace(previous)
	if transcript == "" {
		if len(items) == 0 {
			return compactPreviousSummary(previous)
		}
		return structuredSummaryFromItems(previous, items)
	}
	rawLines := nonEmptySummaryLines(strings.Split(transcript, "\n"))
	if len(rawLines) == 0 {
		if len(items) == 0 {
			return compactPreviousSummary(previous)
		}
		return structuredSummaryFromItems(previous, items)
	}
	toolFileHints := extractTranscriptSummaryFilePaths(rawLines)
	toolCommandHints := extractTranscriptSummaryCommands(rawLines)
	lines := sanitizeTranscriptSummaryLines(rawLines)
	if len(lines) == 0 {
		lines = rawLines
	}

	priority := prioritizeMemoryItems(items)
	priorityCurrent := summarizeItemsByKind(priority.current)
	priorityFiles := summarizeItemsByTags(priority.current, []string{"code-change", "files", "modifications"})
	priorityValidation := summarizeItemsByTags(priority.current, []string{"validation", "build"})
	priorityConstraints := summarizeItemsByKind(priority.constraints)
	priorityWorkflows := summarizeItemsByKind(priority.workflows)
	priorityFacts := summarizeItemsByKind(priority.facts)
	priorHints := compactPreviousSummaryLines(previous)

	contentLines := filterSummaryLines(lines, func(line string) bool {
		return isSubstantiveSummaryLine(line)
	})
	userMessages := collectSummarySection(contentLines, func(line string) bool {
		line = strings.TrimSpace(line)
		return strings.HasPrefix(strings.ToLower(line), "user: ")
	}, 20)
	validationLines := collectSummarySection(append(contentLines, toolCommandHints...), func(line string) bool {
		line = strings.TrimSpace(line)
		trimmed := stripSummaryBulletPrefix(line)
		if !isSubstantiveSummaryLine(trimmed) {
			return false
		}
		lower := strings.ToLower(trimmed)
		return strings.Contains(lower, "go test") || strings.Contains(lower, "go build") || strings.Contains(lower, "gofmt") || strings.Contains(lower, "npm test") || strings.Contains(lower, "pytest") || strings.Contains(lower, "build") || strings.Contains(lower, "validation")
	}, 12)
	recentWork := tailSummaryLines(contentLines, 18)
	recentWork = filterSummaryLines(recentWork, func(line string) bool { return isSubstantiveSummaryLine(line) && !isAssistantMetaSummaryLine(line) })

	filteredPriorHints := sanitizeSummaryOutputLines(filterSummaryLines(priorHints, func(line string) bool {
		trimmed := stripSummaryBulletPrefix(line)
		return trimmed != "" && !isNoisySummaryLine(trimmed) && !isDiscardablePriorSummaryLine(trimmed) && !isTrivialContinuationCandidateLine(trimmed)
	}), false, false)
	primaryCandidates := sanitizeSummaryOutputLines(filterSummaryLines(append(append(priorityCurrent, userMessages...), filteredPriorHints...), func(line string) bool {
		return !isTrivialContinuationCandidateLine(line)
	}), true, false)
	primary := sanitizeSummaryOutputLine(firstSummaryLine(primaryCandidates, firstSummaryLine(recentWork, previous)), true, false)
	technical := sanitizeSummaryOutputLines(dedupeSummaryLines(append(append(append(priorityValidation, priorityConstraints...), priorityWorkflows...), append(validationLines, extractRelevantPriorHints(filteredPriorHints, []string{"technical concepts", "constraints", "validation", "workflow"})...)...)), false, false)
	files := sanitizeSummaryOutputLines(dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(filteredPriorHints, []string{"files", "code sections", "file modifications"})...)), false, false)
	problems := sanitizeSummaryOutputLines(summarizeProblemLines(append(contentLines, extractRelevantPriorHints(filteredPriorHints, []string{"errors", "fixes"})...)), false, false)
	pending := sanitizeSummaryOutputLines(summarizePending(append(contentLines, filteredPriorHints...), compactPreviousSummary(previous)), false, false)
	currentWork := sanitizeSummaryOutputLines(limitSummaryLines(filterSummaryLines(append(append(priorityCurrent, recentWork...), extractRelevantPriorHints(filteredPriorHints, []string{"current work"})...), func(line string) bool {
		return !isTrivialContinuationCandidateLine(line)
	}), 12), false, false)
	if len(currentWork) == 0 && primary != "" {
		currentWork = []string{primary}
	}
	nextStep := sanitizeSummaryOutputLines(inferNextStep(append(append(append(contentLines, priorityCurrent...), toolCommandHints...), filteredPriorHints...), compactPreviousSummary(previous)), false, false)
	problemSolving := sanitizeSummaryOutputLines(limitSummaryLines(priorityFacts, 8), false, false)
	userMessages = sanitizeSummaryOutputLines(limitSummaryLines(userMessages, 20), true, false)

	sections := make([]string, 0, 10)
	if len(filteredPriorHints) > 0 {
		sections = append(sections, "0. Prior Summary Context:\n"+bulletSection(limitSummaryLines(filteredPriorHints, 8)))
	}
	sections = append(sections,
		"1. Primary Request and Intent:\n"+bulletSection(sanitizeSummaryOutputLines([]string{primary}, true, false)),
		"2. Key Technical Concepts:\n"+bulletSection(limitSummaryLines(technical, 10)),
		"3. Files and Code Sections:\n"+bulletSection(limitSummaryLines(files, 12)),
		"4. Errors and Fixes:\n"+bulletSection(limitSummaryLines(problems, 8)),
		"5. Problem Solving:\n"+bulletSection(problemSolving),
		"6. All User Messages:\n"+bulletSection(userMessages),
		"7. Pending Tasks:\n"+bulletSection(limitSummaryLines(pending, 8)),
		"8. Current Work:\n"+bulletSection(currentWork),
		"9. Optional Next Step:\n"+bulletSection(limitSummaryLines(nextStep, 6)),
	)
	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}

func structuredSummaryFromItems(previous string, items []memory.Item) string {
	priority := prioritizeMemoryItems(items)
	previous = compactPreviousSummary(previous)
	priorHints := sanitizeSummaryOutputLines(compactPreviousSummaryLines(previous), false, false)
	primary := sanitizeSummaryOutputLines(summarizeItemsByKind(priority.current), false, false)
	if len(primary) == 0 && len(priorHints) > 0 {
		primary = priorHints
	}
	constraints := sanitizeSummaryOutputLines(limitSummaryLines(append(summarizeItemsByKind(priority.constraints), summarizeItemsByKind(priority.workflows)...), 10), false, false)
	files := sanitizeSummaryOutputLines(limitSummaryLines(summarizeItemsByTags(priority.current, []string{"code-change", "files", "modifications"}), 12), false, false)
	problemSolving := sanitizeSummaryOutputLines(limitSummaryLines(summarizeItemsByKind(priority.facts), 8), false, false)
	pending := sanitizeSummaryOutputLines(limitSummaryLines(append(summarizeItemsByKind(priority.current), extractRelevantPriorHints(priorHints, []string{"pending"})...), 8), false, false)
	currentWork := sanitizeSummaryOutputLines(limitSummaryLines(append(summarizeItemsByKind(priority.current), extractRelevantPriorHints(priorHints, []string{"current work"})...), 12), false, false)
	sections := []string{}
	if len(priorHints) > 0 {
		sections = append(sections, "0. Prior Summary Context:\n"+bulletSection(limitSummaryLines(priorHints, 8)))
	}
	sections = append(sections,
		"1. Primary Request and Intent:\n"+bulletSection(limitSummaryLines(primary, 6)),
		"2. Key Technical Concepts:\n"+bulletSection(constraints),
		"3. Files and Code Sections:\n"+bulletSection(files),
		"4. Errors and Fixes:\n"+bulletSection([]string{"No explicit errors captured in extracted memories."}),
		"5. Problem Solving:\n"+bulletSection(problemSolving),
		"6. All User Messages:\n"+bulletSection([]string{"No direct user transcript retained for this summary pass."}),
		"7. Pending Tasks:\n"+bulletSection(pending),
		"8. Current Work:\n"+bulletSection(currentWork),
		"9. Optional Next Step:\n"+bulletSection([]string{"Continue from extracted current-task and file-change memories."}),
	)
	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}

type prioritizedMemorySummary struct {
	current     []memory.Item
	constraints []memory.Item
	workflows   []memory.Item
	facts       []memory.Item
}

func prioritizeMemoryItems(items []memory.Item) prioritizedMemorySummary {
	var out prioritizedMemorySummary
	for _, item := range items {
		switch item.Kind {
		case memory.KindTask:
			out.current = append(out.current, item)
		case memory.KindConstraint, memory.KindPreference:
			out.constraints = append(out.constraints, item)
		case memory.KindWorkflow:
			out.workflows = append(out.workflows, item)
		default:
			out.facts = append(out.facts, item)
		}
	}
	return out
}

func summarizeItemsByKind(items []memory.Item) []string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		text := summarizeMemoryItemText(item)
		if text == "" {
			continue
		}
		lines = append(lines, text)
	}
	return sanitizeSummaryOutputLines(lines, true, true)
}

func summarizeItemsByTags(items []memory.Item, tags []string) []string {
	want := map[string]bool{}
	for _, tag := range tags {
		want[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		matched := false
		for _, tag := range item.Tags {
			if want[strings.ToLower(strings.TrimSpace(tag))] {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if text := summarizeMemoryItemText(item); text != "" {
			lines = append(lines, text)
		}
	}
	return sanitizeSummaryOutputLines(lines, true, true)
}

func summarizeMemoryItemText(item memory.Item) string {
	text := strings.TrimSpace(item.Text)
	if text == "" {
		return ""
	}
	if hasSummaryTag(item.Tags, "tool-usage") {
		lower := strings.ToLower(text)
		if strings.Contains(lower, "compacted session tools used:") || strings.Contains(lower, "compacted session tool usage:") {
			return ""
		}
	}
	return sanitizeSummaryOutputLine(sanitizeCompactionMemoryText(text), true, true)
}

func sanitizeCompactionMemoryText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "compacted session tools used:") || strings.Contains(lower, "compacted session tool usage:") {
		return ""
	}
	if idx := strings.Index(lower, "compacted session file modifications:"); idx >= 0 {
		rest := strings.TrimSpace(text[idx+len("compacted session file modifications:"):])
		parts := strings.Split(rest, ";")
		cleaned := make([]string, 0, len(parts))
		for _, part := range parts {
			part = sanitizeCompactionModification(part)
			if part != "" {
				cleaned = append(cleaned, part)
			}
		}
		cleaned = dedupeSummaryLines(cleaned)
		if len(cleaned) == 0 {
			return "Compacted session file modifications."
		}
		if len(cleaned) > 6 {
			cleaned = cleaned[:6]
		}
		return "Compacted session file modifications: " + strings.Join(cleaned, "; ") + "."
	}
	if idx := strings.Index(lower, "compacted session validation/build commands run:"); idx >= 0 {
		rest := strings.TrimSpace(text[idx+len("compacted session validation/build commands run:"):])
		parts := strings.Split(rest, ";")
		cleaned := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(strings.TrimSuffix(part, "."))
			part = summaryExcerpt(part, 120)
			if part == "" || !isLikelyValidationCommand(part) || looksLikeSummaryCodeLine(part) {
				continue
			}
			cleaned = append(cleaned, part)
		}
		cleaned = dedupeSummaryLines(cleaned)
		if len(cleaned) == 0 {
			return "Compacted session validation/build commands run."
		}
		if len(cleaned) > 4 {
			cleaned = cleaned[:4]
		}
		return "Compacted session validation/build commands run: " + strings.Join(cleaned, "; ") + "."
	}
	return text
}

func sanitizeCompactionModification(part string) string {
	part = strings.TrimSpace(strings.TrimSuffix(part, "."))
	if part == "" {
		return ""
	}
	if looksLikeSummaryCodeLine(part) || isAssistantMetaSummaryLine(part) || isTrivialContinuationSummaryLine(part) {
		return ""
	}
	replacements := []struct {
		old string
		new string
	}{
		{" (replaced ", " (targeted replacement)"},
		{" (new content includes ", " (added content)"},
	}
	for _, replacement := range replacements {
		if idx := strings.Index(part, replacement.old); idx >= 0 {
			part = part[:idx] + replacement.new
			return summaryExcerpt(part, 140)
		}
	}
	stableSuffixes := []string{" (targeted replacement)", " (added content)", " (removed content)", " (unified diff patch)"}
	for _, suffix := range stableSuffixes {
		if strings.HasSuffix(part, suffix) {
			return summaryExcerpt(part, 140)
		}
	}
	if idx := strings.Index(part, " ("); idx >= 0 {
		part = part[:idx]
	}
	if looksLikeSummaryCodeLine(part) {
		return ""
	}
	return summaryExcerpt(part, 140)
}

func summaryExcerpt(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func extractTranscriptSummaryFilePaths(lines []string) []string {
	paths := make([]string, 0, len(lines))
	seen := map[string]bool{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, match := range summaryFilePathPattern.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			path := strings.TrimSpace(strings.ReplaceAll(match[1], `\\`, `\`))
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

func extractTranscriptSummaryCommands(lines []string) []string {
	commands := make([]string, 0, len(lines))
	seen := map[string]bool{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, match := range summaryCommandPattern.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			command := strings.TrimSpace(strings.ReplaceAll(match[1], `\\`, `\`))
			command = summaryExcerpt(command, 180)
			if command == "" || seen[command] {
				continue
			}
			seen[command] = true
			commands = append(commands, command)
		}
	}
	return commands
}

func sanitizeTranscriptSummaryLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(line, "```") || summaryLineNumberPattern.MatchString(line) || summaryNamedLineNumberPattern.MatchString(line) {
			continue
		}
		if strings.HasPrefix(lower, "assistant: [tool use:") || strings.HasPrefix(lower, "user: [tool result]") {
			continue
		}
		if strings.Contains(lower, `"file_path"`) || strings.Contains(lower, `"path"`) || strings.Contains(lower, `"old_string"`) || strings.Contains(lower, `"new_string"`) || strings.Contains(lower, `"patch_text"`) || strings.Contains(lower, `"tool_id"`) || strings.Contains(lower, `"summary":"tool call preserved as summarized metadata`) {
			continue
		}
		if strings.HasPrefix(lower, "current todos:") || strings.HasPrefix(lower, "retrieved memory:") || strings.HasPrefix(lower, "session summary:") {
			continue
		}
		trimmed := stripSummaryBulletPrefix(line)
		if isDiscardableTranscriptSummaryLine(trimmed) {
			continue
		}
		line = sanitizeCompactionMemoryText(line)
		trimmed = stripSummaryBulletPrefix(line)
		if line == "" || isNoisySummaryLine(line) || isDiscardableTranscriptSummaryLine(trimmed) {
			continue
		}
		out = append(out, line)
	}
	return dedupeSummaryLines(out)
}

func hasSummaryTag(tags []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, tag := range tags {
		if strings.ToLower(strings.TrimSpace(tag)) == want {
			return true
		}
	}
	return false
}

var (
	summaryLineNumberPattern      = regexp.MustCompile(`^\d+\|`)
	summaryNamedLineNumberPattern = regexp.MustCompile(`(?i)^line\s+\d+:`)
	summaryFilePathPattern        = regexp.MustCompile(`"(?:file_path|path)"\s*:\s*"([^"]+)"`)
	summaryCommandPattern         = regexp.MustCompile(`"command"\s*:\s*"([^"]+)"`)
	summaryDiffLinePattern        = regexp.MustCompile(`^(?:[+-]\t|\+\s|@@|diff --git|index\s+[0-9a-f]|---\s|\+\+\+\s)`)
	summaryTodoLinePattern        = regexp.MustCompile(`^\[(?: |✓|→|x)\]`)
	summaryCodeLinePattern        = regexp.MustCompile(`^(?:var|func|if|for|switch|case|return|type|const)\b`)
)

func (a *App) SanitizeLoadedSession(current *session.Session) bool {
	_ = a
	if current == nil {
		return false
	}
	changed := sanitizeLoadedSessionState(current)
	stripped := session.StripEphemeralContextMessages(current.CopyMessages())
	if len(stripped) != len(current.Messages) {
		current.ReplaceMessages(stripped)
		changed = true
	}
	return changed
}

func sanitizeLoadedSessionState(current *session.Session) bool {
	if current == nil {
		return false
	}
	cleanedSummary := sanitizeLoadedSessionSummary(current.Summary)
	if cleanedSummary == strings.TrimSpace(current.Summary) {
		return false
	}
	current.Summary = cleanedSummary
	return true
}

func sanitizeLoadedSessionSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	if !summaryNeedsCleanup(summary) {
		return summary
	}
	return compactPreviousSummary(summary)
}

func summaryNeedsCleanup(summary string) bool {
	lower := strings.ToLower(strings.TrimSpace(summary))
	if lower == "" {
		return false
	}
	markers := []string{
		"compacted session file modifications:",
		"compacted session tool usage:",
		"compacted session tools used:",
		"tool call preserved as summarized metadata",
		"current todos:",
		"\"old_string\"",
		"\"new_string\"",
		"\"patch_text\"",
		"```",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line == "" || isSummarySectionHeader(line) {
			continue
		}
		if summaryLineNumberPattern.MatchString(line) || summaryNamedLineNumberPattern.MatchString(line) || looksLikeSummaryPathLine(line) || looksLikeProceduralSummaryLine(line) || isDiscardablePriorSummaryLine(line) {
			return true
		}
	}
	return false
}

func compactPreviousSummary(previous string) string {
	lines := compactPreviousSummaryLines(previous)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func compactPreviousSummaryLines(previous string) []string {
	if strings.TrimSpace(previous) == "" {
		return nil
	}
	raw := strings.Split(previous, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimSpace(line)
		if line == "" || isSummarySectionHeader(line) || isNoisySummaryLine(line) || isDiscardablePriorSummaryLine(line) {
			continue
		}
		line = sanitizeSummaryOutputLine(line, false, false)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return dedupeSummaryLines(out)
}

func isSummarySectionHeader(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "0. ") || strings.HasPrefix(line, "1. ") || strings.HasPrefix(line, "2. ") || strings.HasPrefix(line, "3. ") || strings.HasPrefix(line, "4. ") || strings.HasPrefix(line, "5. ") || strings.HasPrefix(line, "6. ") || strings.HasPrefix(line, "7. ") || strings.HasPrefix(line, "8. ") || strings.HasPrefix(line, "9. ") {
		return true
	}
	lower := strings.ToLower(line)
	return strings.HasPrefix(lower, "session summary:") || strings.HasPrefix(lower, "retrieved memory:")
}

func isNoisySummaryLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true
	}
	lower := strings.ToLower(line)
	if summaryLineNumberPattern.MatchString(line) || summaryNamedLineNumberPattern.MatchString(line) || strings.HasPrefix(line, "```") {
		return true
	}
	noiseMarkers := []string{
		"retrieved memory:",
		"session summary:",
		"current todos:",
		"tool call preserved as summarized metadata",
		"understood. i'll keep this context in mind",
		"[tool use:",
		"[tool result]",
		"\"name\":\"todowrite\"",
		"todos.json",
		"\"todos\": [",
		"\"summary\":\"tool call preserved as summarized metadata",
		"\"is_error\":",
		"\"tool_id\":",
		"old_string",
		"new_string",
		"patch_text",
		"content replaced in file:",
		"lines changed:",
		"re-run targeted",
		"next step",
		"pending tasks:",
		"optional next step:",
		"problems / pending / next step",
	}
	for _, marker := range noiseMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isDiscardablePriorSummaryLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true
	}
	if summaryDiffLinePattern.MatchString(line) || summaryTodoLinePattern.MatchString(line) || summaryCodeLinePattern.MatchString(line) {
		return true
	}
	if looksLikeSummaryCodeLine(line) || looksLikeSummaryPathLine(line) || looksLikeProceduralSummaryLine(line) {
		return true
	}
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "assistant: ") || strings.HasPrefix(lower, "user: ") {
		return true
	}
	fragments := []string{
		"continue",
		"var ",
		"func ",
		":=",
		"strings.",
		"sdk.",
		"return ",
		"[ ]",
		"[→]",
		"[✓]",
		"已完成：",
		"已通过的定向测试：",
		"旧 session summary",
		"prior summary context",
		"漏网噪声",
		"漏网模式",
		"统一改成",
		"我现在直接",
		"下一步我会",
		"这份 exact sample",
		"注入前清洗",
		"compactprevioussummarylines()",
		"transcript 规则",
		"prior-summary 规则",
		"problems / pending / next step",
	}
	for _, fragment := range fragments {
		if strings.Contains(lower, strings.ToLower(fragment)) || strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

func isDiscardableTranscriptSummaryLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true
	}
	if isBareTrivialContinuationSummaryLine(line) {
		return true
	}
	if summaryDiffLinePattern.MatchString(line) || summaryTodoLinePattern.MatchString(line) || summaryCodeLinePattern.MatchString(line) || summaryNamedLineNumberPattern.MatchString(line) {
		return true
	}
	if looksLikeSummaryCodeLine(line) || looksLikeSummaryPathLine(line) || looksLikeProceduralSummaryLine(line) {
		return true
	}
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "assistant: ") {
		return isAssistantMetaSummaryLine(line)
	}
	fragments := []string{
		"var ",
		"func ",
		":=",
		"strings.",
		"sdk.",
		"return ",
		"[ ]",
		"[→]",
		"[✓]",
		"已完成：",
		"已通过的定向测试：",
		"content replaced in file:",
		"lines changed:",
	}
	for _, fragment := range fragments {
		if strings.Contains(lower, strings.ToLower(fragment)) || strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

func isBareTrivialContinuationSummaryLine(line string) bool {
	line = strings.TrimSpace(line)
	lower := strings.ToLower(line)
	return lower == "continue" || line == "继续"
}

func isTrivialContinuationCandidateLine(line string) bool {
	line = strings.TrimSpace(line)
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "user: ") {
		line = strings.TrimSpace(line[len("user: "):])
	} else if strings.HasPrefix(lower, "assistant: ") {
		line = strings.TrimSpace(line[len("assistant: "):])
	}
	return isBareTrivialContinuationSummaryLine(line)
}

func isTrivialContinuationSummaryLine(line string) bool {
	return isTrivialContinuationCandidateLine(line)
}

func stripSummaryBulletPrefix(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "- ")
	line = strings.TrimPrefix(line, "+ ")
	return strings.TrimSpace(line)
}

func isAssistantMetaSummaryLine(line string) bool {
	line = strings.TrimSpace(line)
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "assistant: ") {
		line = strings.TrimSpace(line[len("assistant: "):])
		lower = strings.ToLower(line)
	}
	metaPhrases := []string{
		"我继续",
		"我先",
		"继续收尾",
		"先把",
		"先补",
		"先跑",
		"直接收尾",
		"继续直接",
		"直接修",
		"当前",
		"这份 summary",
		"现在先",
		"i'll continue",
	}
	for _, phrase := range metaPhrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
}

func looksLikeProceduralSummaryLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	lower := strings.ToLower(line)
	phrases := []string{
		"我现在直接",
		"下一步我会",
		"我刚把",
		"我继续把",
		"我先把",
		"继续把",
		"先把",
		"现在继续",
		"继续直接",
		"去掉代码行 / diff 行 / todo 状态",
		"把 `",
		"这个样本",
		"exact sample",
		"注入前清洗",
		"prior-summary 规则",
		"transcript 规则",
		"todo 状态行",
		"补成回归测试",
		"再跑测试",
		"确保以后不会",
		"完全相信传进来的",
		"喂给模型",
		"re-run targeted",
		"if needed",
		"pending tasks:",
		"optional next step:",
		"problems / pending / next step",
		"content replaced in file:",
		"lines changed:",
	}
	for _, phrase := range phrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return true
		}
	}
	if strings.EqualFold(line, "none") {
		return true
	}
	if strings.IndexFunc(line, unicode.IsSpace) >= 0 && !strings.Contains(line, ": edited") {
		if strings.Contains(lower, "session summary") || strings.Contains(lower, "pending tasks") || strings.Contains(lower, "next step") || strings.Contains(lower, "problems") {
			return true
		}
	}
	if summaryNamedLineNumberPattern.MatchString(line) {
		return true
	}
	return false
}

func looksLikeSummaryCodeLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if summaryLineNumberPattern.MatchString(line) || summaryNamedLineNumberPattern.MatchString(line) || summaryDiffLinePattern.MatchString(line) || strings.HasPrefix(line, "```") {
		return true
	}
	if summaryCodeLinePattern.MatchString(line) {
		return true
	}
	if strings.HasPrefix(line, "\"") && strings.HasSuffix(line, "\"") && !strings.Contains(strings.Trim(line, "\""), " ") {
		return true
	}
	if strings.HasPrefix(line, "`") && strings.HasSuffix(line, "`") {
		return true
	}
	if looksLikeSummaryPathLine(line) {
		return true
	}
	codeMarkers := []string{
		":=",
		" = ",
		"strings.",
		"sdk.",
		"append(",
		"func(",
		"for _,",
		"return ",
		"t.Fatalf(",
		"json.",
		"fmt.",
		"[]string{",
		"map[string]any{",
		"bulletsection(",
		"limitsummarylines(",
		"recordcompactevent(",
		`"old_string"`,
		`"new_string"`,
		`"patch_text"`,
		"content replaced in file:",
		"lines changed:",
	}
	for _, marker := range codeMarkers {
		if strings.Contains(strings.ToLower(line), strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func looksLikeSummaryPathLine(line string) bool {
	line = strings.TrimSpace(strings.TrimSuffix(line, ":"))
	line = strings.Trim(line, "`\"'")
	if line == "" {
		return false
	}
	lower := strings.ToLower(line)
	if strings.Contains(line, `:\`) || strings.Contains(line, `/`) || strings.Contains(line, `\`) {
		if !strings.Contains(line, " ") {
			return true
		}
		for _, ext := range []string{".go", ".txt", ".json", ".md", ".yaml", ".yml"} {
			if strings.Contains(lower, ext) {
				return true
			}
		}
	}
	if !strings.Contains(line, " ") && !strings.Contains(line, ": edited") {
		for _, ext := range []string{".go", ".txt", ".json", ".md", ".yaml", ".yml"} {
			if strings.HasSuffix(lower, ext) {
				return true
			}
		}
		if strings.HasPrefix(lower, "github.com/") || strings.HasPrefix(lower, "internal/") || strings.HasPrefix(lower, "cmd/") || strings.HasPrefix(lower, "unit_tests/") {
			return true
		}
	}
	return false
}

func isLikelyValidationCommand(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}
	prefixes := []string{
		"go test",
		"go build",
		"go vet",
		"gofmt",
		"golangci-lint",
		"npm test",
		"pnpm test",
		"yarn test",
		"pytest",
		"cargo test",
		"mvn test",
		"gradle test",
		"dotnet test",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func isSubstantiveSummaryLine(line string) bool {
	line = strings.TrimSpace(line)
	trimmed := stripSummaryBulletPrefix(line)
	if trimmed == "" || isNoisySummaryLine(trimmed) || isDiscardableTranscriptSummaryLine(trimmed) || looksLikeSummaryCodeLine(trimmed) {
		return false
	}
	if isAssistantMetaSummaryLine(trimmed) {
		return false
	}
	return true
}

func sanitizeSummaryOutputLine(line string, allowUserMessages bool, allowBareCompaction bool) string {
	line = stripSummaryBulletPrefix(line)
	line = sanitizeCompactionMemoryText(line)
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "assistant: ") {
		return ""
	}
	if strings.HasPrefix(lower, "user: ") {
		if !allowUserMessages {
			return ""
		}
		return line
	}
	if isAssistantMetaSummaryLine(line) || isTrivialContinuationCandidateLine(line) || isNoisySummaryLine(line) || looksLikeSummaryCodeLine(line) || looksLikeSummaryPathLine(line) || looksLikeProceduralSummaryLine(line) {
		return ""
	}
	if summaryTodoLinePattern.MatchString(line) || summaryDiffLinePattern.MatchString(line) || summaryLineNumberPattern.MatchString(line) || summaryNamedLineNumberPattern.MatchString(line) || strings.HasPrefix(line, "```") {
		return ""
	}
	noiseMarkers := []string{
		`"old_string"`,
		`"new_string"`,
		`"patch_text"`,
		`"command"`,
		`"file_path"`,
		`"tool_id"`,
		"todos.json",
		"[tool use:",
		"[tool result]",
		"tool call preserved as summarized metadata",
		"旧 session summary",
		"prior summary context",
		"漏网噪声",
		"漏网模式",
		"content replaced in file:",
		"lines changed:",
		"re-run targeted",
		"下一步我会",
		"我现在直接",
		"去掉代码行 / diff 行 / todo 状态",
		"transcript 规则",
		"prior-summary 规则",
		"todo 状态行",
		"补成回归测试",
		"再跑测试",
		"确保以后不会",
		"完全相信传进来的",
		"喂给模型",
		"pending tasks:",
		"optional next step:",
		"none",
	}
	for _, marker := range noiseMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return ""
		}
	}
	if !allowBareCompaction && (line == "Compacted session file modifications." || line == "Compacted session validation/build commands run.") {
		return ""
	}
	return line
}

func sanitizeSummaryOutputLines(lines []string, allowUserMessages bool, allowBareCompaction bool) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = sanitizeSummaryOutputLine(line, allowUserMessages, allowBareCompaction)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return dedupeSummaryLines(out)
}

func filterSummaryLines(lines []string, keep func(string) bool) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if keep != nil && !keep(line) {
			continue
		}
		out = append(out, line)
	}
	return dedupeSummaryLines(out)
}

func extractRelevantPriorHints(lines []string, markers []string) []string {
	if len(lines) == 0 || len(markers) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		for _, marker := range markers {
			if strings.Contains(lower, strings.ToLower(strings.TrimSpace(marker))) {
				out = append(out, line)
				break
			}
		}
	}
	return dedupeSummaryLines(out)
}

func nonEmptySummaryLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func collectSummarySection(lines []string, keep func(string) bool, limit int) []string {
	if limit <= 0 {
		limit = len(lines)
	}
	out := make([]string, 0, limit)
	for _, line := range lines {
		if keep != nil && !keep(line) {
			continue
		}
		out = append(out, line)
		if len(out) >= limit {
			break
		}
	}
	return dedupeSummaryLines(out)
}

func tailSummaryLines(lines []string, limit int) []string {
	lines = nonEmptySummaryLines(lines)
	if limit <= 0 || len(lines) <= limit {
		return lines
	}
	return lines[len(lines)-limit:]
}

func dedupeSummaryLines(lines []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

func limitSummaryLines(lines []string, limit int) []string {
	lines = dedupeSummaryLines(lines)
	if limit <= 0 || len(lines) <= limit {
		return lines
	}
	return lines[:limit]
}

func firstSummaryLine(lines []string, fallback string) string {
	lines = nonEmptySummaryLines(lines)
	if len(lines) > 0 {
		return lines[0]
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return "none"
	}
	return fallback
}

func bulletSection(lines []string) string {
	lines = nonEmptySummaryLines(lines)
	if len(lines) == 0 {
		return "- none"
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "- ") {
			out = append(out, line)
			continue
		}
		out = append(out, "- "+line)
	}
	section := strings.Join(out, "\n")
	if len([]rune(section)) > 6000 {
		section = string([]rune(section)[:6000]) + "..."
	}
	return section
}

func summarizeProblemLines(lines []string) []string {
	matches := collectSummarySection(lines, func(line string) bool {
		line = strings.TrimSpace(line)
		trimmed := stripSummaryBulletPrefix(line)
		if trimmed == "" || isNoisySummaryLine(trimmed) || isDiscardableTranscriptSummaryLine(trimmed) || looksLikeSummaryCodeLine(trimmed) {
			return false
		}
		lower := strings.ToLower(trimmed)
		return strings.Contains(lower, "error") || strings.Contains(lower, "fail") || strings.Contains(lower, "panic") || strings.Contains(lower, "bug") || strings.Contains(lower, "fix")
	}, 12)
	if len(matches) == 0 {
		return []string{"No explicit errors captured in current transcript."}
	}
	return matches
}

func summarizePending(lines []string, previous string) []string {
	matches := collectSummarySection(lines, func(line string) bool {
		line = strings.TrimSpace(line)
		trimmed := stripSummaryBulletPrefix(line)
		if trimmed == "" || isNoisySummaryLine(trimmed) || isDiscardableTranscriptSummaryLine(trimmed) || looksLikeSummaryCodeLine(trimmed) || isTrivialContinuationCandidateLine(trimmed) {
			return false
		}
		lower := strings.ToLower(trimmed)
		return strings.Contains(lower, "todo") || strings.Contains(lower, "pending") || strings.Contains(lower, "next") || strings.Contains(trimmed, "下一步")
	}, 12)
	if len(matches) > 0 {
		return matches
	}
	if previous != "" {
		prevLines := collectSummarySection(strings.Split(previous, "\n"), func(line string) bool {
			line = strings.TrimSpace(line)
			trimmed := stripSummaryBulletPrefix(line)
			if trimmed == "" || isNoisySummaryLine(trimmed) || isDiscardablePriorSummaryLine(trimmed) || looksLikeSummaryCodeLine(trimmed) || isTrivialContinuationCandidateLine(trimmed) {
				return false
			}
			lower := strings.ToLower(trimmed)
			return strings.Contains(lower, "pending") || strings.Contains(lower, "next step") || strings.Contains(lower, "current work")
		}, 8)
		if len(prevLines) > 0 {
			return prevLines
		}
	}
	return []string{"No explicit pending tasks captured."}
}

func inferNextStep(lines []string, previous string) []string {
	matches := collectSummarySection(lines, func(line string) bool {
		line = strings.TrimSpace(line)
		trimmed := stripSummaryBulletPrefix(line)
		if trimmed == "" || isNoisySummaryLine(trimmed) || isDiscardableTranscriptSummaryLine(trimmed) || looksLikeSummaryCodeLine(trimmed) || isTrivialContinuationCandidateLine(trimmed) {
			return false
		}
		lower := strings.ToLower(trimmed)
		return strings.Contains(lower, "next") || strings.Contains(lower, "should") || strings.Contains(lower, "need to") || strings.Contains(trimmed, "下一步")
	}, 8)
	if len(matches) > 0 {
		return matches
	}
	if strings.TrimSpace(previous) != "" {
		return []string{"Continue from prior summary context and latest retained work."}
	}
	return []string{"Resume from the latest user request and retained recent work."}
}

func TestOnlySummarizeForContext(transcript, previous string) string {
	return summarizeForContext(transcript, previous, nil)
}

func TestOnlySummarizeForContextWithItems(transcript, previous string, items []memory.Item) string {
	return summarizeForContext(transcript, previous, items)
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

func memoryModelName(cfg config.Config) string {
	if strings.TrimSpace(cfg.FastModel) != "" {
		return strings.TrimSpace(cfg.FastModel)
	}
	return cfg.Model
}

func engineConfig(cfg config.Config, client *cpanthropic.Client, runtime *hook.Runtime, registry *tool.Registry, permissions *permission.Service, onTextDelta, onThinkingDelta func(string), onToolStart func(name string, input json.RawMessage), onToolDone func(name string, output string, isError bool), onUsage func(engine.Usage), onAskUser func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)) engine.Config {
	skillRegistry := loadSkills(cfg)
	return engine.Config{
		Client:           client,
		Hooks:            runtime,
		Tools:            registry,
		Permissions:      permissions,
		ModelName:        cfg.Model,
		FastModelName:    cfg.FastModel,
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
		tool.NewViewImageTool(),
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
