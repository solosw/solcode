package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/changegraph"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/lsp"
	"github.com/solosw/solcode/internal/mcp"
	"github.com/solosw/solcode/internal/memory"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/sandbox"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/skill"
	"github.com/solosw/solcode/internal/tool"
	"github.com/solosw/solcode/internal/workflow"
)

type App struct {
	Config           config.Config
	Client           *cpanthropic.Client
	Tools            *tool.Registry
	Hooks            *hook.Runtime
	Permissions      *permission.Service
	Engine           *engine.Engine
	Coordinator      *agent.Coordinator
	Sessions         *session.Manager
	MemoryStore      *memory.FileStore
	MemoryManager    *memory.Manager
	SkillRegistry    *skill.Registry
	WorkflowRegistry *workflow.Registry
	MCPRegistry      *mcp.Registry
	lspManager       *lsp.Manager
	ChangeGraph      *changegraph.Store
	summaryWriter    session.SummaryWriter
	summaryRefresh   sync.Mutex
	mcpFactory       mcp.ClientFactory
	mcpLoadMu        sync.Mutex
	mcpLoaded        bool

	// usageSession binds OnUsage accumulation to the active session so
	// token totals persist across reloads.
	usageSessionMu sync.Mutex
	usageSession   *session.Session

	onTextDelta     func(string)
	onThinkingDelta func(string)
	onToolStart     func(name string, input json.RawMessage, toolUseID string)
	onToolDone      func(name string, output string, isError bool, toolUseID string)
	onUsage         func(engine.Usage)
	onStatus        func(string)
	onAgentProgress func(tool.AgentProgressEvent)
	onModeChange    func(permission.Mode) error
	onAskUser       func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)
	textFileSystem  tool.TextFileSystem
	queuedPrompts   func() []string
}

type Option func(*options)

type options struct {
	mcpFactory      mcp.ClientFactory
	onTextDelta     func(string)
	onThinkingDelta func(string)
	onToolStart     func(name string, input json.RawMessage, toolUseID string)
	onToolDone      func(name string, output string, isError bool, toolUseID string)
	onUsage         func(engine.Usage)
	onStatus        func(string)
	onAgentProgress func(tool.AgentProgressEvent)
	onModeChange    func(permission.Mode) error
	onAskUser       func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)
	textFileSystem  tool.TextFileSystem
	queuedPrompts   func() []string
}

func buildToolState(cfg config.Config, mcpFactory mcp.ClientFactory) (*tool.Registry, *skill.Registry, *mcp.Registry, *lsp.Manager, error) {
	registry := tool.NewRegistry()
	lspManager := newLSPManager(cfg)
	registerBuiltins(registry, lspManager, cfg.Sandbox, cfg.Image, cfg.ImageEnabled())

	skillRegistry := loadSkills(cfg)
	if defs := skillRegistry.All(); len(defs) > 0 {
		registry.Register(tool.NewSkillTool(skillRegistry))
	}
	// ToolSearch queries live registries at invocation time, so dynamic MCP and
	// Skill capabilities remain discoverable without all schemas being sent.
	registry.Register(tool.NewToolSearchTool(registry, skillRegistry))

	mcpRegistry := mcp.NewRegistry(cfg.MCP.Servers)
	if mcpFactory != nil {
		mcpRegistry.SetClientFactory(mcpFactory)
	}
	return registry, skillRegistry, mcpRegistry, lspManager, nil
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

func WithToolCallbacks(onToolStart func(name string, input json.RawMessage, toolUseID string), onToolDone func(name string, output string, isError bool, toolUseID string)) Option {
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

func WithAgentProgressCallback(onAgentProgress func(tool.AgentProgressEvent)) Option {
	return func(o *options) {
		o.onAgentProgress = onAgentProgress
	}
}

func WithModeChangeCallback(onModeChange func(permission.Mode) error) Option {
	return func(o *options) {
		o.onModeChange = onModeChange
	}
}

func WithTextFileSystem(textFileSystem tool.TextFileSystem) Option {
	return func(o *options) {
		o.textFileSystem = textFileSystem
	}
}

func WithAskUserCallback(onAskUser func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)) Option {
	return func(o *options) {
		o.onAskUser = onAskUser
	}
}

// WithQueuedPrompts supplies messages submitted while the main agent is working.
// The callback is consumed after the active batch of tool calls completes.
func WithQueuedPrompts(queuedPrompts func() []string) Option {
	return func(o *options) {
		o.queuedPrompts = queuedPrompts
	}
}

func New(cfg config.Config, opts ...Option) (*App, error) {
	var options options
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	registry, skillRegistry, mcpRegistry, lspManager, err := buildToolState(cfg, options.mcpFactory)
	if err != nil {
		return nil, err
	}
	graphStore, err := openChangeGraph(cfg)
	if err != nil {
		// Knowledge graph is optional; do not block startup when it fails.
		fmt.Fprintf(os.Stderr, "warning: knowledge graph disabled: %v\n", err)
		graphStore = nil
	}

	runtime := hook.NewRuntime(cfg.Hooks)
	permissions := permission.NewServiceWithConfig(cfg.Permissions)
	permissions.SetMode(cfg.PermissionMode)
	client := cpanthropic.NewClient(cpanthropic.Options{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Format:  cfg.APIFormat,
	})

	recordFileChange := newFileChangeRecorder(graphStore)

	// Construct App first so engine OnUsage can bind to emitUsage for session totals.
	application := &App{
		Config:           cfg,
		Client:           client,
		Tools:            registry,
		Hooks:            runtime,
		Permissions:      permissions,
		Sessions:         nil, // filled below when enabled
		MemoryStore:      nil,
		MemoryManager:    nil,
		SkillRegistry:    skillRegistry,
		WorkflowRegistry: loadWorkflows(cfg),
		MCPRegistry:      mcpRegistry,
		lspManager:       lspManager,
		ChangeGraph:      graphStore,
		mcpFactory:       options.mcpFactory,
		onTextDelta:      options.onTextDelta,
		onThinkingDelta:  options.onThinkingDelta,
		onToolStart:      options.onToolStart,
		onToolDone:       options.onToolDone,
		onUsage:          options.onUsage,
		onStatus:         options.onStatus,
		onAgentProgress:  options.onAgentProgress,
		onModeChange:     options.onModeChange,
		onAskUser:        options.onAskUser,
		textFileSystem:   options.textFileSystem,
		queuedPrompts:    options.queuedPrompts,
	}
	eng := engine.NewEngine(engineConfig(cfg, client, runtime, registry, permissions, options.onTextDelta, options.onThinkingDelta, options.onToolStart, options.onToolDone, application.emitUsage, options.onStatus, options.onAgentProgress, options.onAskUser, options.textFileSystem, options.queuedPrompts, recordFileChange, application.compactMessagesMidRun))
	coordinator := agent.NewCoordinator(eng)
	subagent := tool.NewSubagentTool(coordinator)
	registry.Register(subagent, tool.NewTaskToolWithSubagent(subagent))
	registry.Register(tool.NewModeSwitchToolWithGoal(application.SwitchMode, application.startGoalFlow))
	application.Engine = eng
	application.Coordinator = coordinator

	if cfg.Session.Enabled && cfg.Session.Persist {
		application.Sessions = session.NewManager(session.NewFileStore(cfg.Session.Dir), session.SessionID(cfg.Session.DefaultSession))
	}
	if cfg.Memory.Enabled {
		memoryStore := memory.NewFileStore(cfg.Memory.Dir)
		memoryModel := memoryModelName(cfg)
		application.MemoryStore = memoryStore
		application.MemoryManager = memory.NewManagerWithExtractor(
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
		// Let the model decide when a fact is worth remembering, and let it
		// look up what was remembered before instead of re-deriving it.
		registry.Register(tool.NewWriteMemoryTool(application), tool.NewReadMemoryTool(application))
	}
	registry.Register(tool.NewReadObservationTool(application))

	return application, nil
}

// EnsureMCPTools starts configured MCP servers once and adds their discovered
// tools to the execution registry. It is safe to call concurrently from startup
// prewarming and a user's first prompt.
func (a *App) startGoalFlow(ctx context.Context, uctx *tool.UseContext) error {
	if a == nil {
		return fmt.Errorf("app is nil")
	}
	if uctx == nil {
		return fmt.Errorf("goal flow context is missing")
	}
	_, err := a.RunGoalWithSession(ctx, uctx.SessionID, "", uctx.WorkDir, a.Config.MaxTurns)
	return err
}

func (a *App) EnsureMCPTools(ctx context.Context) error {
	if a == nil || a.MCPRegistry == nil {
		return nil
	}
	a.mcpLoadMu.Lock()
	defer a.mcpLoadMu.Unlock()
	if a.mcpLoaded {
		return nil
	}
	err := a.MCPRegistry.LoadContext(ctx)
	if tools := a.MCPRegistry.Tools(); len(tools) > 0 && a.Tools != nil {
		a.Tools.Register(tools...)
	}
	// Preserve best-effort startup behavior: an unavailable server does not
	// prevent the application from accepting prompts.
	a.mcpLoaded = true
	return err
}

// emitUsage accumulates per-turn usage into the active session (when bound)
// and forwards absolute session totals to the UI callback.
func (a *App) emitUsage(u engine.Usage) {
	if a == nil {
		return
	}
	a.usageSessionMu.Lock()
	if a.usageSession != nil {
		a.usageSession.Metadata.Usage.Add(
			u.InputTokens,
			u.OutputTokens,
			u.CacheCreationInputTokens,
			u.CacheReadInputTokens,
		)
		// Forward absolute session totals so the TUI can display/persist them.
		u.InputTokens = a.usageSession.Metadata.Usage.InputTokens
		u.OutputTokens = a.usageSession.Metadata.Usage.OutputTokens
		u.CacheCreationInputTokens = a.usageSession.Metadata.Usage.CacheCreationInputTokens
		u.CacheReadInputTokens = a.usageSession.Metadata.Usage.CacheReadInputTokens
	}
	cb := a.onUsage
	a.usageSessionMu.Unlock()
	if cb != nil {
		cb(u)
	}
}

// bindUsageSession attaches s for OnUsage accumulation. Call the returned
// cleanup to detach (typically via defer).
func (a *App) bindUsageSession(s *session.Session) func() {
	if a == nil {
		return func() {}
	}
	a.usageSessionMu.Lock()
	a.usageSession = s
	a.usageSessionMu.Unlock()
	return func() {
		a.usageSessionMu.Lock()
		if a.usageSession == s {
			a.usageSession = nil
		}
		a.usageSessionMu.Unlock()
	}
}

func openChangeGraph(cfg config.Config) (*changegraph.Store, error) {
	if !cfg.KnowledgeGraph.Enabled {
		return nil, nil
	}
	graphPath := strings.TrimSpace(cfg.KnowledgeGraph.Dir)
	if graphPath == "" {
		graphPath = config.DefaultKnowledgeGraphPath(cfg.WorkDir)
	} else {
		graphPath = filepath.Join(graphPath, "knowledge.db")
	}
	store, err := changegraph.OpenWithOptions(graphPath, changegraph.Options{
		RetentionDays: cfg.KnowledgeGraph.RetentionDays,
		MaxEvents:     cfg.KnowledgeGraph.MaxEvents,
		MaxDatabaseMB: cfg.KnowledgeGraph.MaxDatabaseMB,
	})
	if err != nil {
		return nil, fmt.Errorf("open knowledge graph: %w", err)
	}
	return store, nil
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	var firstErr error
	if a.Sessions != nil {
		if err := a.Sessions.ReleaseAll(); err != nil {
			firstErr = err
		}
	}
	if a.lspManager != nil {
		_ = a.lspManager.Close()
	}
	if a == nil {
		return nil
	}
	if a.MCPRegistry != nil {
		firstErr = a.MCPRegistry.Close()
	}
	if a.ChangeGraph != nil {
		if err := a.ChangeGraph.Close(); firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *App) SwitchMode(ctx context.Context, target string) error {
	if a == nil || a.Permissions == nil {
		return fmt.Errorf("permission service is not configured")
	}
	next := permission.NormalizeMode(permission.Mode(target))
	if next != permission.ModePlan && next != permission.ModeBypass && next != permission.ModeGoal {
		return fmt.Errorf("mode must be plan, bypass, or goal")
	}
	current := a.Permissions.Mode()
	if current == next {
		return nil
	}
	if next == permission.ModePlan {
		if !a.approveModeTransition("enter plan mode") {
			return fmt.Errorf("user denied transition to plan mode")
		}
	} else {
		if current != permission.ModePlan {
			return fmt.Errorf("%s mode can only be entered from plan mode", next)
		}
		if !a.approveModeTransition("leave plan mode and enter " + string(next) + " mode") {
			return fmt.Errorf("user denied transition to %s mode", next)
		}
	}
	if a.onModeChange != nil {
		if err := a.onModeChange(next); err != nil {
			return fmt.Errorf("persist permission mode: %w", err)
		}
	}
	a.Permissions.SetMode(next)
	a.Config.PermissionMode = next
	a.Config.Permissions.Mode = next
	if a.onStatus != nil {
		a.onStatus("Permission mode: " + string(next))
	}
	return nil
}

func (a *App) approveModeTransition(description string) bool {
	if a == nil || a.Permissions == nil {
		return false
	}
	return a.Permissions.RequestApproval(tool.ModeSwitchToolName, description)
}

func (a *App) SwitchModel(cfg config.Config) error {
	if a == nil {
		return fmt.Errorf("app is nil")
	}
	client := a.Client
	if cfg.APIKey != a.Config.APIKey || cfg.BaseURL != a.Config.BaseURL || cpanthropic.NormalizeFormat(cfg.APIFormat) != a.Client.Format() {
		client = cpanthropic.NewClient(cpanthropic.Options{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Format: cfg.APIFormat})
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
	a.Engine.UpdateConfig(engineConfig(cfg, client, a.Hooks, a.Tools, a.Permissions, a.onTextDelta, a.onThinkingDelta, a.onToolStart, a.onToolDone, a.emitUsage, a.onStatus, a.onAgentProgress, a.onAskUser, a.textFileSystem, a.queuedPrompts, newFileChangeRecorder(a.ChangeGraph), a.compactMessagesMidRun))
	return nil
}

func (a *App) ReloadFeatures(cfg config.Config, mcpFactory mcp.ClientFactory) error {
	if a == nil {
		return fmt.Errorf("app is nil")
	}
	if mcpFactory == nil {
		mcpFactory = a.mcpFactory
	}
	registry, skillRegistry, mcpRegistry, lspManager, err := buildToolState(cfg, mcpFactory)
	if err != nil {
		return err
	}
	graphStore, err := openChangeGraph(cfg)
	if err != nil {
		_ = mcpRegistry.Close()
		return err
	}
	if a.MCPRegistry != nil {
		_ = a.MCPRegistry.Close()
	}
	if a.lspManager != nil {
		_ = a.lspManager.Close()
	}
	if a.ChangeGraph != nil {
		_ = a.ChangeGraph.Close()
	}
	a.Config = cfg
	a.Tools = registry
	a.SkillRegistry = skillRegistry
	a.WorkflowRegistry = loadWorkflows(cfg)
	a.MCPRegistry = mcpRegistry
	a.lspManager = lspManager
	a.ChangeGraph = graphStore
	subagent := tool.NewSubagentTool(a.Coordinator)
	registry.Register(subagent, tool.NewTaskToolWithSubagent(subagent))
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
		registry.Register(tool.NewWriteMemoryTool(a), tool.NewReadMemoryTool(a))
	} else {
		a.MemoryManager = nil
	}
	a.Engine.UpdateConfig(engineConfig(cfg, a.Client, a.Hooks, a.Tools, a.Permissions, a.onTextDelta, a.onThinkingDelta, a.onToolStart, a.onToolDone, a.emitUsage, a.onStatus, a.onAgentProgress, a.onAskUser, a.textFileSystem, a.queuedPrompts, newFileChangeRecorder(a.ChangeGraph), a.compactMessagesMidRun))
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

func (a *App) RepairSession(ctx context.Context, sessionID, workDir string) (*session.Session, int, error) {
	if a == nil || a.Sessions == nil {
		return nil, 0, fmt.Errorf("sessions are not enabled")
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = a.Config.Session.DefaultSession
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "main"
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = a.Config.WorkDir
	}
	current, err := a.Sessions.LoadOrCreate(ctx, session.SessionID(sessionID), workDir, a.Config.Model)
	if err != nil {
		return nil, 0, fmt.Errorf("load session: %w", err)
	}
	repaired, removed := session.RepairMessages(current.CopyMessages())
	changed := removed > 0 || len(repaired) != len(current.Messages)
	if changed {
		current.ReplaceMessages(repaired)
	}
	if a.SanitizeLoadedSession(current) {
		changed = true
		// SanitizeLoadedSession may remove stale tool-use blocks after the first pass.
		if repaired, extraRemoved := session.RepairMessages(current.CopyMessages()); extraRemoved > 0 || len(repaired) != len(current.Messages) {
			current.ReplaceMessages(repaired)
			removed += extraRemoved
		}
	}
	if changed {
		if err := a.Sessions.Save(context.WithoutCancel(ctx), current); err != nil {
			return nil, 0, fmt.Errorf("save repaired session: %w", err)
		}
	}
	return current, removed, nil
}

const mainAgentMaxRetries = 5

var mainAgentRetryDelay = 10 * time.Second

func (a *App) runMainAgent(ctx context.Context, run func() agent.AgentResult) agent.AgentResult {
	var lastResult agent.AgentResult
	for attempt := 0; attempt <= mainAgentMaxRetries; attempt++ {
		result := run()
		if result.Error == "" || ctx.Err() != nil || !retryableMainAgentError(result.Error) {
			return result
		}
		lastResult = result
		if attempt == mainAgentMaxRetries {
			break
		}
		retry := attempt + 1
		if a.onStatus != nil {
			a.onStatus(fmt.Sprintf("retry %d/%d", retry, mainAgentMaxRetries))
		}
		if err := waitForMainAgentRetry(ctx, mainAgentRetryDelay); err != nil {
			return agent.AgentResult{AgentID: result.AgentID, Error: err.Error()}
		}
	}
	return lastResult
}

func (a *App) runMainAgentWithHistory(ctx context.Context, run func() engine.RunResult) engine.RunResult {
	var lastResult engine.RunResult
	for attempt := 0; attempt <= mainAgentMaxRetries; attempt++ {
		result := run()
		if result.AgentResult.Error == "" || ctx.Err() != nil || !retryableMainAgentError(result.AgentResult.Error) {
			return result
		}
		lastResult = result
		if attempt == mainAgentMaxRetries {
			break
		}
		retry := attempt + 1
		if a.onStatus != nil {
			a.onStatus(fmt.Sprintf("retry %d/%d", retry, mainAgentMaxRetries))
		}
		if err := waitForMainAgentRetry(ctx, mainAgentRetryDelay); err != nil {
			return engine.RunResult{AgentResult: agent.AgentResult{AgentID: result.AgentResult.AgentID, Error: err.Error()}, Messages: result.Messages}
		}
	}
	return lastResult
}

func retryableMainAgentError(errText string) bool {
	errText = strings.TrimSpace(errText)
	return errText != "" && !strings.Contains(errText, context.Canceled.Error()) && !strings.Contains(errText, context.DeadlineExceeded.Error())
}

func waitForMainAgentRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *App) RunPrompt(ctx context.Context, prompt, workDir string, maxTurns int) (agent.AgentResult, error) {
	if a == nil {
		return agent.AgentResult{}, fmt.Errorf("app is nil")
	}
	_ = a.EnsureMCPTools(ctx)
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
	return a.runMainAgent(ctx, func() agent.AgentResult {
		return a.Engine.Run(ctx, cfg)
	}), nil
}

func (a *App) RunPromptWithSession(ctx context.Context, sessionID, prompt, workDir string, maxTurns int) (agent.AgentResult, error) {
	if a == nil {
		return agent.AgentResult{}, fmt.Errorf("app is nil")
	}
	_ = a.EnsureMCPTools(ctx)
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
	current, newSession, err := a.Sessions.LoadOrCreateWithStatus(ctx, session.SessionID(sessionID), workDir, a.Config.Model)
	if err != nil {
		return agent.AgentResult{}, fmt.Errorf("load session: %w", err)
	}
	sessionStateChanged := a.SanitizeLoadedSession(current)
	if a.shouldCompact(ctx, current) {
		changed, err := a.compactSession(ctx, current, false)
		if err != nil {
			return agent.AgentResult{}, fmt.Errorf("compact session with AI summary: %w", err)
		}
		if changed {
			current.Metadata.MemoryCompactionCompleted = true
			current.Metadata.MemoryCompactionMessageCount = len(current.Messages)
			sessionStateChanged = true
		}
	}
	if sessionStateChanged {
		if err := a.Sessions.Save(context.WithoutCancel(ctx), current); err != nil {
			return agent.AgentResult{}, fmt.Errorf("save session after compaction: %w", err)
		}
	}
	memoryContext, err := a.retrieveNewSessionMemoryContext(ctx, prompt, current, newSession)
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
	projectKnowledge := a.projectKnowledgeForRequest(ctx, current, prompt)
	// Bind usage accumulation so OnUsage persists session totals.
	unbindUsage := a.bindUsageSession(current)
	defer unbindUsage()
	result := a.runMainAgentWithHistory(ctx, func() engine.RunResult {
		return a.Engine.RunWithHistory(ctx, engine.RunRequest{
			AgentConfig:      cfg,
			SessionID:        sessionID,
			Messages:         current.CopyMessages(),
			SessionSummary:   sessionSummaryForRequest(current),
			MemoryContext:    memoryContext,
			ProjectKnowledge: projectKnowledge,
		})
	})
	current.Metadata.WorkDir = workDir
	current.Metadata.Model = a.Config.Model
	if len(result.Messages) > 0 {
		current.ReplaceMessages(session.StripEphemeralContextMessages(result.Messages))
	}
	if result.AgentResult.Error == "" && a.Config.Memory.Enabled {
		a.rememberExplicitMemory(ctx, prompt, sessionID)
	}
	a.resetMemoryMaintenanceCycleIfBelowThreshold(ctx, current)
	refreshSummary := result.AgentResult.Error == "" && a.Config.Memory.Enabled && a.shouldRefreshMemorySummary(ctx, current)
	if refreshSummary {
		// Persist the cycle marker before starting the detached request so another
		// prompt cannot enqueue the same summary job again.
		current.Metadata.MemorySummaryCompleted = true
	}
	saveCtx := context.WithoutCancel(ctx)
	if err := a.Sessions.Save(saveCtx, current); err != nil {
		return result.AgentResult, fmt.Errorf("save session: %w", err)
	}
	if refreshSummary {
		a.refreshSessionSummaryInBackground(session.SessionID(sessionID), workDir)
	}
	return result.AgentResult, nil
}

func (a *App) projectKnowledgeContext(ctx context.Context, current *session.Session, prompt string) string {
	if a == nil || a.ChangeGraph == nil || current == nil || !a.Config.KnowledgeGraph.Enabled {
		return ""
	}
	maxTokens := a.Config.KnowledgeGraph.ContextMaxTokens
	if maxTokens <= 0 {
		maxTokens = 4000
	}
	maxCharacters := maxTokens * 4
	var parts []string
	if todos := activeTodos(config.DefaultTodoPath(current.Metadata.WorkDir)); len(todos) > 0 {
		var b strings.Builder
		b.WriteString("## Active tasks\n")
		for _, todo := range todos {
			b.WriteString(fmt.Sprintf("- [%s] %s\n", todo.Status, todo.Content))
		}
		parts = append(parts, strings.TrimSpace(b.String()))
	}
	// Cross-session project knowledge is only injected when the session opted in.
	// Declining cross-session memory on /new-session must keep this session isolated.
	graphContext, err := a.ChangeGraph.BuildRelevantContext(ctx, string(current.Metadata.ID), prompt, maxCharacters, sessionAllowsCrossSessionMemory(current))
	if err == nil && graphContext != "" {
		parts = append(parts, graphContext)
	}
	contextText := strings.Join(parts, "\n\n")
	if len([]rune(contextText)) <= maxCharacters {
		return contextText
	}
	if compacted, err := a.compactProjectKnowledge(ctx, contextText, maxTokens); err == nil && compacted != "" {
		return compacted
	}
	// Preserve active Todo items first when model compaction is unavailable.
	return string([]rune(contextText)[:maxCharacters])
}

func (a *App) compactProjectKnowledge(ctx context.Context, contextText string, maxTokens int) (string, error) {
	if a == nil || a.Client == nil {
		return "", fmt.Errorf("knowledge context compactor is unavailable")
	}
	model := strings.TrimSpace(a.Config.FastModel)
	if model == "" {
		model = a.Config.Model
	}
	message, err := a.Client.Create(ctx, cpanthropic.MessageRequest{
		Model:     model,
		MaxTokens: int64(maxTokens),
		System:    "Compress project knowledge context for a coding agent. Preserve all in-progress Todo items, pending Todo items, latest described file changes, file paths, symbols, and timestamps. Remove duplication and return only concise Markdown.",
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(contextText))},
		Thinking:  false,
		Stream:    false,
	})
	if err != nil {
		return "", err
	}
	compacted := strings.TrimSpace(cpanthropic.TextFromMessage(message))
	maxCharacters := maxTokens * 4
	if len([]rune(compacted)) > maxCharacters {
		compacted = string([]rune(compacted)[:maxCharacters])
	}
	return compacted, nil
}

func activeTodos(path string) []tool.TodoItem {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var todos []tool.TodoItem
	if json.Unmarshal(data, &todos) != nil {
		return nil
	}
	out := make([]tool.TodoItem, 0, len(todos))
	for _, todo := range todos {
		if todo.Status == "in_progress" || todo.Status == "pending" {
			out = append(out, todo)
		}
	}
	return out
}

// retrieveNewSessionMemoryContext no longer injects retrieved memory into
// ordinary turns. Memory-related context enters the model only via durable
// compaction messages (session summary / project knowledge) after context
// has been compacted. Cross-session recall remains available through the
// ReadMemory tool and post-compact durable context.
func (a *App) retrieveNewSessionMemoryContext(ctx context.Context, prompt string, current *session.Session, newSession bool) ([]engine.ContextItem, error) {
	if current != nil && current.Metadata.MemoryBootstrapPending {
		// Clear the legacy one-shot flag so it cannot re-arm older sessions.
		current.Metadata.MemoryBootstrapPending = false
	}
	return nil, nil
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

func (a *App) refreshSessionSummaryInBackground(sessionID session.SessionID, workDir string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := a.refreshSessionSummary(ctx, sessionID, workDir); err != nil {
			a.recordCompactEvent("summary_refresh_failed", map[string]any{
				"session_id": string(sessionID),
				"error":      err.Error(),
			})
			a.resetSessionSummaryMarker(sessionID, workDir)
		}
	}()
}

func (a *App) refreshSessionSummary(ctx context.Context, sessionID session.SessionID, workDir string) error {
	if a == nil || a.Sessions == nil {
		return fmt.Errorf("sessions are not enabled")
	}
	a.summaryRefresh.Lock()
	defer a.summaryRefresh.Unlock()
	writer := a.sessionSummaryWriter()
	if writer == nil {
		return fmt.Errorf("session summary AI writer is unavailable")
	}
	current, err := a.Sessions.LoadOrCreate(ctx, sessionID, workDir, a.Config.Model)
	if err != nil {
		return fmt.Errorf("load session for summary refresh: %w", err)
	}
	transcript := session.Transcript(session.StripEphemeralContextMessages(current.CopyMessages()))
	if strings.TrimSpace(transcript) == "" {
		return fmt.Errorf("session summary transcript is empty")
	}
	summary, err := writer.Summarize(ctx, sanitizeLoadedSessionSummary(current.Summary), transcript)
	if err != nil {
		return fmt.Errorf("generate session summary with AI: %w", err)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return fmt.Errorf("generate session summary with AI: empty response")
	}

	// Reload after the model call so a background refresh never overwrites
	// messages written by a newer foreground turn.
	latest, err := a.Sessions.LoadOrCreate(ctx, sessionID, workDir, a.Config.Model)
	if err != nil {
		return fmt.Errorf("reload session after summary refresh: %w", err)
	}
	latest.Summary = summary
	latest.Metadata.MemorySummaryCompleted = true
	if err := a.Sessions.Save(context.WithoutCancel(ctx), latest); err != nil {
		return fmt.Errorf("save AI session summary: %w", err)
	}
	a.recordCompactEvent("summary_refresh_succeeded", map[string]any{
		"session_id":    string(sessionID),
		"summary_runes": len([]rune(summary)),
	})
	return nil
}

func (a *App) resetSessionSummaryMarker(sessionID session.SessionID, workDir string) {
	if a == nil || a.Sessions == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	current, err := a.Sessions.LoadOrCreate(ctx, sessionID, workDir, a.Config.Model)
	if err != nil {
		return
	}
	current.Metadata.MemorySummaryCompleted = false
	_ = a.Sessions.Save(ctx, current)
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

// EstimateSessionContextTokens returns the same context occupancy the TUI
// footer uses for "ctx used" (system+tools+messages via ContextBuilder).
func (a *App) EstimateSessionContextTokens(ctx context.Context, current *session.Session) int64 {
	return int64(a.estimateSessionContextTokens(ctx, current))
}

func (a *App) estimateSessionContextTokens(ctx context.Context, current *session.Session) int {
	if a == nil || current == nil {
		return 0
	}
	messages := session.StripEphemeralContextMessages(current.CopyMessages())
	builder := engine.ContextBuilder{
		SystemPrompt: a.Config.SystemPrompt,
		ProjectRules: config.FormatProjectRulesPrompt(config.LoadProjectRules(a.Config.WorkDir)),
		Skills:       skillInfos(a.SkillRegistry),
		SkillNames:   skillNames(a.SkillRegistry),
		PlanMode:     a.Permissions != nil && a.Permissions.Mode() == permission.ModePlan,
	}
	tools := []tool.Tool(nil)
	if a.Tools != nil {
		// Estimate with the compact dynamic set so the status bar matches
		// what the next model request will actually send.
		tools = engine.SelectToolsForTurn(a.Tools.All(), nil, "", nil)
	}
	return int(builder.EstimateContextTokens(engine.BuildRequest{
		Model:            a.Config.Model,
		MaxTokens:        a.Config.MaxTokens,
		WorkDir:          current.Metadata.WorkDir,
		Messages:         messages,
		Tools:            tools,
		Thinking:         a.Config.Thinking,
		ThinkingText:     a.Config.ThinkingText,
		Effort:           a.Config.Effort,
		Stream:           a.Config.Stream,
		SessionSummary:   sessionSummaryForRequest(current),
		ProjectKnowledge: a.projectKnowledgeForRequest(ctx, current, current.Summary),
	}))
}

// sessionSummaryForRequest never injects summary into ordinary turns.
// After compaction, the summary already lives as a durable leading user
// message (Compacted session summary:). Pre-compact chat stays pure so
// prompt-cache prefixes are not rewritten every request.
func sessionSummaryForRequest(current *session.Session) string {
	return ""
}

// projectKnowledgeForRequest never injects project knowledge into ordinary
// turns. Compaction captures the current graph/todos into a durable leading
// user message (Compacted project knowledge:); until then, normal dialogue
// does not carry this dynamic memory context.
func (a *App) projectKnowledgeForRequest(ctx context.Context, current *session.Session, prompt string) string {
	return ""
}

func persistCompactedContext(current *session.Session, summary, projectKnowledge string) {
	if current == nil {
		return
	}
	current.Summary = strings.TrimSpace(summary)
	current.ReplaceMessages(session.CompactedContextMessages(current.Summary, projectKnowledge))
}

func (a *App) compactedProjectKnowledge(ctx context.Context, current *session.Session, summary string) string {
	if current == nil {
		return ""
	}
	return a.projectKnowledgeContext(ctx, current, summary)
}

// compactMessagesMidRun force-compacts an in-flight message list when the engine
// estimates the composed context has reached MaxContextTokens mid-run.
func (a *App) compactMessagesMidRun(ctx context.Context, messages []sdk.MessageParam) ([]sdk.MessageParam, error) {
	if a == nil {
		return messages, nil
	}
	if len(messages) == 0 {
		return messages, nil
	}
	temp := &session.Session{
		Metadata: session.Metadata{
			ID:      session.SessionID("mid-run"),
			WorkDir: a.Config.WorkDir,
			Model:   a.Config.Model,
		},
	}
	temp.ReplaceMessages(append([]sdk.MessageParam(nil), messages...))

	// Avoid flipping the TUI to "Ready" while the agent is still running.
	prevStatus := a.onStatus
	if prevStatus != nil {
		a.onStatus = func(status string) {
			if status == "Ready" {
				prevStatus("Thinking…")
				return
			}
			prevStatus(status)
		}
		defer func() { a.onStatus = prevStatus }()
	}

	if _, err := a.compactSession(ctx, temp, true); err != nil {
		return nil, err
	}
	out := temp.CopyMessages()
	if len(out) == 0 {
		return messages, fmt.Errorf("mid-run compaction produced empty history")
	}
	return out, nil
}

func (a *App) CompactSession(ctx context.Context, sessionID, workDir string) (*session.Session, bool, error) {
	if a == nil {
		return nil, false, fmt.Errorf("app is nil")
	}
	if a.Sessions == nil {
		return nil, false, fmt.Errorf("sessions are not enabled")
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
	changed, err := a.compactSession(ctx, current, true)
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

type aiSessionSummaryWriter struct {
	client *cpanthropic.Client
	model  string
}

func (w aiSessionSummaryWriter) Summarize(ctx context.Context, previous, transcript string) (string, error) {
	if w.client == nil {
		return "", fmt.Errorf("session summary client is unavailable")
	}
	input := strings.TrimSpace(transcript)
	if previous = strings.TrimSpace(previous); previous != "" {
		input = "Previous session summary:\n" + previous + "\n\nSession transcript to summarize:\n" + input
	}
	message, err := w.client.Create(ctx, cpanthropic.MessageRequest{
		Model:     w.model,
		MaxTokens: 2000,
		System: strings.Join([]string{
			"Summarize a coding-agent session for the next turn.",
			"Return only the summary, with no preamble or analysis.",
			"Preserve the user's active request, decisions, completed work, exact file paths and symbols when important, validation results, errors, and unresolved follow-up tasks.",
			"Do not reproduce tool-call JSON, raw logs, code listings, or role prefixes.",
			"Be concise and factual; do not invent details.",
		}, " "),
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(input))},
		Thinking: false,
		Stream:   false,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(cpanthropic.TextFromMessage(message)), nil
}

func (a *App) sessionSummaryWriter() session.SummaryWriter {
	if a == nil {
		return nil
	}
	if a.summaryWriter != nil {
		return a.summaryWriter
	}
	if a.Client == nil {
		return nil
	}
	return aiSessionSummaryWriter{client: a.Client, model: memoryModelName(a.Config)}
}

func (a *App) observationStoreFor(current *session.Session) session.ObservationStore {
	if a == nil || current == nil {
		return nil
	}
	dir := strings.TrimSpace(a.Config.Session.Dir)
	if dir == "" {
		dir = config.DefaultSessionDir(a.Config.WorkDir)
	}
	if dir == "" {
		return nil
	}
	return session.NewFileObservationStore(session.ObservationStoreDir(dir, current.Metadata.ID))
}

func (a *App) observationStoreForID(sessionID, workDir string) session.ObservationStore {
	if a == nil {
		return nil
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		id = strings.TrimSpace(a.Config.Session.DefaultSession)
	}
	if id == "" {
		id = "main"
	}
	dir := strings.TrimSpace(a.Config.Session.Dir)
	if dir == "" {
		if workDir == "" {
			workDir = a.Config.WorkDir
		}
		dir = config.DefaultSessionDir(workDir)
	}
	if dir == "" {
		return nil
	}
	return session.NewFileObservationStore(session.ObservationStoreDir(dir, session.SessionID(id)))
}

func observationLookupRoot(sessionDir, workDir string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir != "" {
		return filepath.Clean(sessionDir)
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return ""
	}
	return filepath.Clean(config.DefaultSessionDir(workDir))
}

func observationPathAllowed(path, sessionDir, workDir string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	if root := observationLookupRoot(sessionDir, workDir); root != "" {
		if tool.CheckWithinWorkDir(root, path) == nil {
			return true
		}
	}
	if workDir = strings.TrimSpace(workDir); workDir != "" {
		if tool.CheckWithinWorkDir(workDir, path) == nil {
			return true
		}
	}
	return false
}

// ReadObservation implements tool.ObservationReader so the model can recover
// compacted tool results from a placeholder, observation_id, or stored path.
func (a *App) ReadObservation(_ context.Context, req tool.ObservationReadRequest) (string, error) {
	if a == nil {
		return "", fmt.Errorf("app is nil")
	}
	id, path := session.ParseObservationRef(req.Ref)
	if strings.TrimSpace(req.ID) != "" {
		id = strings.TrimSpace(req.ID)
	}
	if strings.TrimSpace(req.Path) != "" {
		path = strings.TrimSpace(req.Path)
	}
	if id == "" && path == "" && strings.TrimSpace(req.Ref) != "" {
		id = strings.TrimSpace(req.Ref)
	}
	if id == "" && path == "" {
		return "", fmt.Errorf("observation reference is empty")
	}

	sessionDir := strings.TrimSpace(a.Config.Session.Dir)
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = a.Config.WorkDir
	}
	if sessionDir == "" {
		sessionDir = config.DefaultSessionDir(workDir)
	}

	store := a.observationStoreForID(req.SessionID, workDir)
	if id != "" && store != nil {
		content, err := store.Load(id)
		if err == nil && strings.TrimSpace(content) != "" {
			return content, nil
		}
	}
	if path != "" {
		if !observationPathAllowed(path, sessionDir, workDir) {
			return "", fmt.Errorf("observation path is outside the session store")
		}
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return "", fmt.Errorf("read observation path: %w", err)
		}
		return string(data), nil
	}
	if id == "" {
		return "", fmt.Errorf("observation reference is empty")
	}
	return "", fmt.Errorf("observation %q not found", id)
}

func (a *App) compactSession(ctx context.Context, current *session.Session, force bool) (bool, error) {
	if a == nil || current == nil {
		return false, nil
	}
	cleanedMessages := session.StripEphemeralContextMessages(current.CopyMessages())
	cleanedHistory := len(cleanedMessages) != len(current.Messages)
	if cleanedHistory {
		current.ReplaceMessages(cleanedMessages)
	}
	messagesToCompact := session.StripCompactedContextMessages(cleanedMessages)
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
	summaryGate := a.memorySummaryTriggerTokens()
	if summaryGate <= 0 {
		summaryGate = trigger
	}
	result, err := session.Compact(ctx, current.Summary, messagesToCompact, a.sessionSummaryWriter(), session.CompactOptions{
		MaxRecentTurns:         a.Config.Memory.MaxRecentTurns,
		SummaryThresholdTokens: trigger,
		TargetTokens:           target,
		EstimatedTokens:        estimated,
		Force:                  force,
		ObservationStore:       a.observationStoreFor(current),
		// Zero uses session.defaultRecentUnmasked (newest 2 user turns).
		ObservationKeepTurns: 0,
		SummaryIfAboveTokens: summaryGate,
	})
	if err != nil {
		a.recordCompactEvent("compact_failed", map[string]any{
			"session_id": string(current.Metadata.ID),
			"error":      err.Error(),
		})
		return false, err
	}
	previousSummary := sanitizeLoadedSessionSummary(current.Summary)
	if !result.Changed {
		if estimated < trigger && !force {
			a.recordCompactEvent("compact_skipped", map[string]any{
				"session_id":       string(current.Metadata.ID),
				"estimated_tokens": estimated,
				"trigger_tokens":   trigger,
			})
			return cleanedHistory, nil
		}
		// The total request can cross the threshold because session history,
		// project knowledge, tools, and the system prompt are composed together.
		// Even when Headroom does not rewrite the message slice, fold the old
		// session history into Summary so the next request is exactly:
		// project knowledge + session summary + latest user prompt.
		current.Summary = result.Summary
		if force || strings.TrimSpace(current.Summary) == "" || (current.Summary == previousSummary && strings.TrimSpace(result.OriginalTranscript) != "") {
			current.Summary = conciseSessionSummary(result.OriginalTranscript, previousSummary)
		}
		if strings.TrimSpace(current.Summary) == "" {
			a.recordCompactEvent("compact_skipped", map[string]any{
				"session_id": string(current.Metadata.ID),
				"reason":     "no substantive session summary",
			})
			return cleanedHistory, nil
		}
		if err := a.rememberCompactedSession(ctx, current, previousSummary, current.Summary, result, estimated); err != nil {
			a.recordCompactEvent("memory_extract_failed", map[string]any{
				"session_id": string(current.Metadata.ID),
				"reason":     "compaction",
				"error":      err.Error(),
			})
		}
		persistCompactedContext(current, current.Summary, a.compactedProjectKnowledge(ctx, current, current.Summary))
		a.recordCompactEvent("compact_succeeded", map[string]any{
			"session_id":      string(current.Metadata.ID),
			"messages_before": len(cleanedMessages),
			"messages_after":  0,
			"summary_runes":   len([]rune(current.Summary)),
			"trigger_source":  "composed_context",
		})
		return true, nil
	}
	beforeMessages := len(current.Messages)
	maskedOnly := result.Changed && strings.Contains(session.Transcript(result.Messages), session.ObservationMaskMarker) && strings.TrimSpace(result.Summary) == previousSummary
	if maskedOnly {
		current.ReplaceMessages(session.MergeCompactedContext(result.Messages, current.CopyMessages()))
		a.recordCompactEvent("compact_succeeded", map[string]any{
			"session_id":      string(current.Metadata.ID),
			"messages_before": beforeMessages,
			"messages_after":  len(current.Messages),
			"method":          "observation_masking",
			"summary_runes":   len([]rune(current.Summary)),
			"original_runes":  len([]rune(result.OriginalTranscript)),
			"retained_runes":  len([]rune(result.RetainedTranscript)),
		})
		return true, nil
	}
	nextSummary := strings.TrimSpace(result.Summary)
	if force || nextSummary == "" {
		nextSummary = conciseSessionSummary(result.OriginalTranscript, previousSummary)
	}
	if strings.TrimSpace(nextSummary) == "" {
		a.recordCompactEvent("compact_skipped", map[string]any{
			"session_id": string(current.Metadata.ID),
			"reason":     "no substantive session summary",
		})
		return cleanedHistory, nil
	}
	if err := a.rememberCompactedSession(ctx, current, previousSummary, nextSummary, result, estimated); err != nil {
		a.recordCompactEvent("memory_extract_failed", map[string]any{
			"session_id": string(current.Metadata.ID),
			"reason":     "compaction",
			"error":      err.Error(),
		})
	}
	persistCompactedContext(current, nextSummary, a.compactedProjectKnowledge(ctx, current, nextSummary))
	retainedTokens := a.estimateSessionContextTokens(ctx, current)
	a.recordCompactEvent("compact_succeeded", map[string]any{
		"session_id":          string(current.Metadata.ID),
		"messages_before":     beforeMessages,
		"messages_after":      len(current.Messages),
		"summary_runes":       len([]rune(current.Summary)),
		"original_runes":      len([]rune(result.OriginalTranscript)),
		"compacted_runes":     len([]rune(result.CompactedTranscript)),
		"retained_runes":      len([]rune(result.RetainedTranscript)),
		"discarded_runes":     len([]rune(result.DiscardedTranscript)),
		"retained_tokens":     retainedTokens,
		"used_local_fallback": false,
	})
	return true, nil
}

// conciseSessionSummary preserves the recent conversational outcome from old
// session messages. Project knowledge remains a separate context block.
func conciseSessionSummary(transcript, previous string) string {
	conversation := conciseConversationLines(transcript)
	prior := compactPreviousSummaryLines(previous)
	combined := append(limitSummaryLines(prior, 3), tailSummaryLines(conversation, 6)...)
	combined = dedupeSummaryLines(combined)
	if len(combined) == 0 {
		return ""
	}
	return "Recent session state:\n" + bulletSection(limitSummaryLines(combined, 6))
}

func conciseConversationLines(transcript string) []string {
	lines := make([]string, 0, 8)
	userFallback := make([]string, 0, 4)
	skipToolBlock := false
	currentRole := ""
	for _, raw := range nonEmptySummaryLines(strings.Split(transcript, "\n")) {
		line := stripSummaryBulletPrefix(raw)
		lower := strings.ToLower(line)
		content := line
		switch {
		case strings.HasPrefix(lower, "user: "):
			currentRole = "user"
			skipToolBlock = false
			content = strings.TrimSpace(line[len("user: "):])
		case strings.HasPrefix(lower, "assistant: "):
			currentRole = "assistant"
			skipToolBlock = false
			content = strings.TrimSpace(line[len("assistant: "):])
		}
		contentLower := strings.ToLower(content)
		if strings.HasPrefix(contentLower, "[tool use:") || strings.HasPrefix(contentLower, "[tool result]") {
			skipToolBlock = true
			continue
		}
		if skipToolBlock {
			continue
		}
		if currentRole == "user" && content != "" && !isBareTrivialContinuationSummaryLine(content) {
			userFallback = append(userFallback, summaryExcerpt(content, 240))
		}
		if currentRole == "assistant" && isAssistantMetaSummaryLine(content) {
			continue
		}
		if content == "" || isBareTrivialContinuationSummaryLine(content) {
			continue
		}
		if strings.Contains(contentLower, `"path"`) || strings.Contains(contentLower, `"old_string"`) || strings.Contains(contentLower, `"new_string"`) || strings.Contains(contentLower, `"patch_text"`) || strings.Contains(contentLower, `"tool_id"`) {
			continue
		}
		if isNoisySummaryLine(content) || looksLikeSummaryCodeLine(content) {
			continue
		}
		lines = append(lines, summaryExcerpt(content, 240))
	}
	if len(lines) == 0 {
		return dedupeSummaryLines(userFallback)
	}
	return dedupeSummaryLines(lines)
}

func (a *App) rememberCompactedSession(ctx context.Context, current *session.Session, previousSummary, nextSummary string, result session.CompactResult, estimatedTokens int) error {
	if a == nil || current == nil || a.MemoryManager == nil || !a.Config.Memory.Enabled {
		return nil
	}
	_, err := a.MemoryManager.RememberExtracted(ctx, memory.ExtractionInput{
		SourceSessionID:     string(current.Metadata.ID),
		WorkDir:             current.Metadata.WorkDir,
		PreviousSummary:     previousSummary,
		NewSummary:          nextSummary,
		Transcript:          result.OriginalTranscript,
		OriginalTranscript:  result.OriginalTranscript,
		CompactedTranscript: result.CompactedTranscript,
		RetainedTranscript:  result.RetainedTranscript,
		DiscardedTranscript: result.DiscardedTranscript,
		TriggerReason:       "compaction",
		EstimatedTokens:     estimatedTokens,
	})
	return err
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
	// Legacy compaction summaries store file facts as "path: edited". Accept
	// that narrow, normalized form before generic code/path detection rejects it.
	if strings.Contains(strings.ToLower(part), ": edited") {
		if idx := strings.Index(part, " ("); idx >= 0 {
			part = part[:idx]
		}
		return summaryExcerpt(part, 140)
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
		if strings.Contains(lower, `"path"`) || strings.Contains(lower, `"path"`) || strings.Contains(lower, `"old_string"`) || strings.Contains(lower, `"new_string"`) || strings.Contains(lower, `"patch_text"`) || strings.Contains(lower, `"tool_id"`) || strings.Contains(lower, `"summary":"tool call preserved as summarized metadata`) {
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
	if current == nil {
		return false
	}
	changed := sanitizeLoadedSessionState(current)
	repaired, removed := session.RepairMessages(current.CopyMessages())
	if removed > 0 || len(repaired) != len(current.Messages) {
		current.ReplaceMessages(repaired)
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
	if isPlaceholderSessionSummary(summary) {
		return ""
	}
	if !summaryNeedsCleanup(summary) {
		return summary
	}
	return compactPreviousSummary(summary)
}

func isPlaceholderSessionSummary(summary string) bool {
	lower := strings.ToLower(strings.TrimSpace(summary))
	return strings.Contains(lower, "文件变更图上下文") &&
		strings.Contains(lower, "旧 session 对话压缩结果") &&
		strings.Contains(lower, "用户最新 prompt")
}

func summaryNeedsCleanup(summary string) bool {
	lower := strings.ToLower(strings.TrimSpace(summary))
	if lower == "" {
		return false
	}
	markers := []string{
		"recent session state:",
		"(file has ",
		"more lines. use 'offset' parameter",
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
		if line == "" || isSummarySectionHeader(line) {
			continue
		}
		if isSafeLegacySourcePath(line) {
			out = append(out, line)
			continue
		}
		legacyFileFact := strings.Contains(strings.ToLower(line), "compacted session file modifications:")
		// Normalize legacy compaction facts before generic path/code filters;
		// otherwise their embedded file paths make useful history look like
		// source-code noise.
		line = sanitizeCompactionMemoryText(line)
		if line == "" || isNoisySummaryLine(line) || (!legacyFileFact && isDiscardablePriorSummaryLine(line)) {
			continue
		}
		if legacyFileFact {
			out = append(out, line)
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
	return strings.HasPrefix(lower, "session summary:") ||
		strings.HasPrefix(lower, "recent session state:") ||
		strings.HasPrefix(lower, "retrieved memory:")
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
		"(file has ",
		"more lines. use 'offset' parameter",
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
	if isTrivialContinuationCandidateLine(line) {
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
		"文件变更图上下文",
		"旧 session 对话压缩结果",
		"用户最新 prompt",
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

func isSafeLegacySourcePath(line string) bool {
	line = strings.TrimSpace(strings.Trim(line, "`\"'"))
	if line == "" || strings.ContainsAny(line, " \\:") || strings.Contains(line, "..") {
		return false
	}
	lower := strings.ToLower(line)
	if !(strings.HasPrefix(lower, "internal/") || strings.HasPrefix(lower, "cmd/") || strings.HasPrefix(lower, "unit_tests/")) {
		return false
	}
	for _, ext := range []string{".go", ".json", ".md", ".yaml", ".yml"} {
		if strings.HasSuffix(lower, ext) {
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
		`"path"`,
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

// sessionAllowsCrossSessionMemory reports whether this session opted into
// reading memories / project knowledge produced by other sessions.
// Unset (nil) means the session never went through the /new-session prompt
// and is treated as opted-out of cross-session memory injection.
func sessionAllowsCrossSessionMemory(current *session.Session) bool {
	if current == nil || current.Metadata.CrossSessionMemory == nil {
		return false
	}
	return *current.Metadata.CrossSessionMemory
}

// shouldRetrieveNewSessionMemory gates the one-shot memory bootstrap that runs
// only for sessions that explicitly enabled cross-session memory.
func shouldRetrieveNewSessionMemory(current *session.Session, newSession bool) bool {
	if !sessionAllowsCrossSessionMemory(current) {
		return false
	}
	return newSession || current.Metadata.MemoryBootstrapPending
}

func (a *App) recordCompactEvent(kind string, fields map[string]any) {
	if a == nil {
		return
	}
	path := filepath.Join(config.ProjectStateDir(a.Config.WorkDir), "compact.log")
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

func memoryModelName(cfg config.Config) string {
	if strings.TrimSpace(cfg.FastModel) != "" {
		return strings.TrimSpace(cfg.FastModel)
	}
	return cfg.Model
}

func engineConfig(cfg config.Config, client *cpanthropic.Client, runtime *hook.Runtime, registry *tool.Registry, permissions *permission.Service, onTextDelta, onThinkingDelta func(string), onToolStart func(name string, input json.RawMessage, toolUseID string), onToolDone func(name string, output string, isError bool, toolUseID string), onUsage func(engine.Usage), onStatus func(string), onAgentProgress func(tool.AgentProgressEvent), onAskUser func(ctx context.Context, params tool.AskUserParams) (map[string]string, error), textFileSystem tool.TextFileSystem, queuedPrompts func() []string, recordFileChange func(ctx context.Context, uctx *tool.UseContext, change tool.FileChange), compactMessages func(ctx context.Context, messages []sdk.MessageParam) ([]sdk.MessageParam, error)) engine.Config {
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
		ProjectRules:     config.FormatProjectRulesPrompt(config.LoadProjectRules(cfg.WorkDir)),
		Skills:           skillInfos(skillRegistry),
		SkillNames:       skillNames(skillRegistry),
		SkillRoots:       skillRoots(skillRegistry),
		SkillRootsByName: skillRootsByName(skillRegistry),
		MaxTurns:         cfg.MaxTurns,
		Stream:           cfg.Stream,
		Thinking:         cfg.Thinking,
		ThinkingText:     cfg.ThinkingText,
		Effort:           cfg.Effort,
		TodoPath:         config.DefaultTodoPath(cfg.WorkDir),
		TextFileSystem:   textFileSystem,
		OnTextDelta:      onTextDelta,
		OnThinkingDelta:  onThinkingDelta,
		OnToolStart:      onToolStart,
		OnToolDone:       onToolDone,
		OnStatus:         onStatus,
		OnAgentProgress:  onAgentProgress,
		OnUsage:          onUsage,
		OnAskUser:        onAskUser,
		QueuedPrompts:    queuedPrompts,
		RecordFileChange: recordFileChange,
		CompactMessages:  compactMessages,
	}
}

func newFileChangeRecorder(store *changegraph.Store) func(context.Context, *tool.UseContext, tool.FileChange) {
	if store == nil {
		return nil
	}
	return func(ctx context.Context, uctx *tool.UseContext, change tool.FileChange) {
		workDir := ""
		sessionID := ""
		if uctx != nil {
			workDir = uctx.WorkDir
			sessionID = uctx.SessionID
		}
		path := change.Path
		if rel, err := filepath.Rel(workDir, path); err == nil {
			path = rel
		}
		// Recording is intentionally fail-open: a successful user edit must not
		// fail because optional project knowledge could not be persisted.
		_ = store.RecordFileChange(ctx, changegraph.FileChange{
			SessionID:   sessionID,
			ToolName:    change.ToolName,
			Path:        path,
			Description: change.Description,
			Before:      change.Before,
			After:       change.After,
		})
	}
}

func registerBuiltins(registry *tool.Registry, lspManager *lsp.Manager, sandboxPolicy sandbox.Policy, imageCfg config.ImageConfig, imageEnabled bool) {
	tools := []tool.Tool{
		tool.NewAskUserTool(),
		tool.NewBashToolWithSandbox(sandboxPolicy),
		tool.NewDiffTool(),
		tool.NewEditTool(),
		tool.NewMultiEditTool(),
		tool.NewMultiWriteTool(),
		tool.NewFetchTool(),
		tool.NewGlobTool(),
		tool.NewGrepTool(),
		tool.NewLsTool(),
		tool.NewPatchTool(),
		tool.NewTodoWriteTool(),
		tool.NewViewImageTool(),
		tool.NewViewTool(),
		tool.NewWaitTool(),
		tool.NewWebSearchTool(),
		tool.NewWriteTool(),
	}
	if imageEnabled {
		api := tool.ImageAPIConfig{
			BaseURL:    imageCfg.BaseURL,
			APIKey:     imageCfg.APIKey,
			Model:      imageCfg.Model,
			EditModel:  imageCfg.EditModel,
			Size:       imageCfg.Size,
			Quality:    imageCfg.Quality,
			TimeoutSec: imageCfg.TimeoutSec,
			OutputDir:  imageCfg.OutputDir,
		}
		tools = append(tools, tool.NewImageGenerateTool(api), tool.NewImageEditTool(api))
	}
	if lspManager != nil {
		tools = append(tools, tool.NewLSPTool(lspManager))
	}
	registry.Register(tools...)
}

func newLSPManager(cfg config.Config) *lsp.Manager {
	if !cfg.LSPEnabled() {
		return nil
	}
	user := make([]lsp.ServerCommand, 0, len(cfg.LSP.Servers))
	for _, s := range cfg.LSP.Servers {
		user = append(user, lsp.ServerCommand{
			Language:   s.Language,
			Extensions: append([]string(nil), s.Extensions...),
			Command:    append([]string(nil), s.Command...),
		})
	}
	return lsp.NewManagerFromCommands(user, cfg.LSPIncludeDefaults())
}

func loadWorkflows(cfg config.Config) *workflow.Registry {
	registry := workflow.NewRegistry()
	for _, dir := range cfg.Workflows.Paths {
		registryDir := dir
		if !filepath.IsAbs(registryDir) && cfg.WorkDir != "" {
			registryDir = filepath.Join(cfg.WorkDir, registryDir)
		}
		loaded := workflow.LoadFromDirs(registryDir)
		for _, def := range loaded.All() {
			if len(cfg.Workflows.Enabled) > 0 && !contains(cfg.Workflows.Enabled, def.Name) {
				continue
			}
			if contains(cfg.Workflows.Disabled, def.Name) {
				continue
			}
			// Skip invalid definitions at load time.
			if err := def.Validate(); err != nil {
				continue
			}
			registry.Add(def)
		}
	}
	return registry
}

// RunWorkflow executes a named user-authored Task graph explicitly.
// Workflows are not exposed as model tools. Starting a workflow switches permission mode to bypass.
// Results are returned as text only; nothing is written to memory.
func (a *App) RunWorkflow(ctx context.Context, name, args string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("app is nil")
	}
	// Workflows always run with full tool access.
	if a.Permissions != nil {
		a.Permissions.SetMode(permission.ModeBypass)
	}
	if a.Coordinator == nil {
		return "", fmt.Errorf("agent coordinator is not configured")
	}
	if a.WorkflowRegistry == nil {
		return "", fmt.Errorf("no workflows loaded")
	}
	def, ok := a.WorkflowRegistry.Find(name)
	if !ok {
		return "", fmt.Errorf("unknown workflow: %s", strings.TrimSpace(name))
	}
	params, err := def.ToTaskParams(args)
	if err != nil {
		return "", err
	}
	params.FastModel = a.Config.FastModel
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal workflow tasks: %w", err)
	}
	taskTool := tool.NewTaskTool(a.Coordinator)
	result, err := taskTool.Invoke(ctx, &tool.UseContext{
		AgentID:   "workflow",
		WorkDir:   a.Config.WorkDir,
		FastModel: a.Config.FastModel,
		Status:    a.onStatus,
	}, raw)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("workflow %q returned no result", def.Name)
	}
	textOut := strings.TrimSpace(result.Text)
	if result.IsError {
		if textOut == "" {
			textOut = "workflow failed"
		}
		return "", fmt.Errorf("%s", textOut)
	}
	if textOut == "" {
		textOut = fmt.Sprintf("Workflow %q completed with no output.", def.Name)
	}
	return fmt.Sprintf("[Workflow: %s]\n\n%s", def.Name, textOut), nil
}

// ListWorkflows returns loaded workflow names and descriptions for explicit UI listing.
func (a *App) ListWorkflows() []workflow.Definition {
	if a == nil || a.WorkflowRegistry == nil {
		return nil
	}
	return a.WorkflowRegistry.All()
}

// ReloadWorkflows reloads workflow definitions from configured paths.
func (a *App) ReloadWorkflows() {
	if a == nil {
		return
	}
	a.WorkflowRegistry = loadWorkflows(a.Config)
}

// SaveWorkflow writes a workflow definition to the user or project workflows directory
// and reloads the in-memory registry.
func (a *App) SaveWorkflow(def workflow.Definition, scope workflow.SaveScope) (string, error) {
	return a.SaveWorkflowWithLayout(def, scope, nil)
}

// SaveWorkflowWithLayout writes workflow.yaml and optional layout.json for the node editor.
func (a *App) SaveWorkflowWithLayout(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error) {
	if a == nil {
		return "", fmt.Errorf("app is nil")
	}
	dir, err := workflowDirForScope(a.Config, scope)
	if err != nil {
		return "", err
	}
	path, err := workflow.SaveToDirWithLayout(def, dir, layout)
	if err != nil {
		return "", err
	}
	a.ReloadWorkflows()
	return path, nil
}

// DeleteWorkflow removes a loaded workflow from disk (when under allowed roots)
// and reloads the registry.
func (a *App) DeleteWorkflow(name string) error {
	if a == nil {
		return fmt.Errorf("app is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("workflow name is required")
	}
	if a.WorkflowRegistry == nil {
		return fmt.Errorf("no workflows loaded")
	}
	def, ok := a.WorkflowRegistry.Find(name)
	if !ok {
		return fmt.Errorf("unknown workflow: %s", name)
	}
	if err := workflow.Delete(def, workflowDeleteRoots(a.Config)); err != nil {
		return err
	}
	a.ReloadWorkflows()
	return nil
}

func workflowDeleteRoots(cfg config.Config) []string {
	roots := append([]string(nil), cfg.Workflows.Paths...)
	if user := filepath.Join(config.UserConfigDir(), "workflows"); user != "" {
		roots = append(roots, user)
	}
	if project := config.ProjectConfigDir(cfg.WorkDir); project != "" {
		roots = append(roots, filepath.Join(project, "workflows"))
	}
	return roots
}

func workflowDirForScope(cfg config.Config, scope workflow.SaveScope) (string, error) {
	switch scope {
	case workflow.SaveScopeUser:
		return filepath.Join(config.UserConfigDir(), "workflows"), nil
	case workflow.SaveScopeProject:
		projectDir := config.ProjectConfigDir(cfg.WorkDir)
		if projectDir == "" {
			return "", fmt.Errorf("project workflow directory requires work_dir")
		}
		return filepath.Join(projectDir, "workflows"), nil
	default:
		return "", fmt.Errorf("unknown workflow save scope %q", scope)
	}
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

func skillInfos(registry *skill.Registry) []engine.SkillInfo {
	if registry == nil {
		return nil
	}
	defs := registry.All()
	out := make([]engine.SkillInfo, 0, len(defs))
	for _, def := range defs {
		out = append(out, engine.SkillInfo{
			Name:        def.Name,
			Description: def.Description,
		})
	}
	return out
}

func skillRoots(registry *skill.Registry) []string {
	if registry == nil {
		return nil
	}
	defs := registry.All()
	out := make([]string, 0, len(defs))
	seen := map[string]bool{}
	for _, def := range defs {
		if !def.IsPackage() {
			continue
		}
		root := strings.TrimSpace(def.Root())
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

func skillRootsByName(registry *skill.Registry) map[string]string {
	if registry == nil {
		return nil
	}
	out := map[string]string{}
	for _, def := range registry.All() {
		if !def.IsPackage() {
			continue
		}
		if root := strings.TrimSpace(def.Root()); root != "" {
			out[def.Name] = root
		}
	}
	return out
}
