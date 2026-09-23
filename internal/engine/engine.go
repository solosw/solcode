package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/attach"
	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/skill"
	"github.com/solosw/solcode/internal/tokenest"
	"github.com/solosw/solcode/internal/tool"
)

type Model interface {
	Send(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

type ModelRequest struct {
	Prompt  string
	WorkDir string
}

type ModelResponse struct {
	Text string
}

type Usage struct {
	EstimatedContextTokens   int64
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	MaxContextTokens         int64
}

// MessageClient is the subset of anthropic.Client used by the engine loop.
type MessageClient interface {
	Create(ctx context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error)
}

type Config struct {
	Model            Model
	Client           MessageClient
	Hooks            *hook.Runtime
	Tools            *tool.Registry
	Permissions      *permission.Service
	ModelName        string
	FastModelName    string
	MaxContextTokens int64
	MaxTokens        int64
	SystemPrompt     string
	// ProjectRules are project-only instructions loaded from
	// <workDir>/.solcode (rules.md and rules/*.md) at startup.
	ProjectRules     string
	Skills           []SkillInfo
	SkillNames       []string          // legacy; used only when Skills is empty
	SkillRoots       []string          // absolute skill package roots for fallback path resolution
	SkillRootsByName map[string]string // activated skill name -> absolute package root
	// SkillRegistry resolves a skill name to its Definition, so a
	// router-selected skill can be force-loaded without a Skill tool round trip.
	SkillRegistry    *skill.Registry
	MaxTurns         int
	Stream           bool
	Thinking         bool
	ThinkingText     bool
	Effort           string
	TodoPath         string
	TextFileSystem   tool.TextFileSystem
	OnTextDelta      func(string)
	OnThinkingDelta  func(string)
	OnToolStart      func(name string, input json.RawMessage, toolUseID string)
	OnToolDone       func(name string, output string, isError bool, toolUseID string)
	OnStatus         func(string)
	OnAgentProgress  func(tool.AgentProgressEvent)
	OnUsage          func(Usage)
	OnAskUser        func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)
	// AskUserAutoSelect answers AskUser without a human (Jev or first-option
	// fallback). Used for nested agents and AskUser timeouts.
	AskUserAutoSelect func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)
	// OnTodosUpdated records a session-memory todolist snapshot after each
	// successful TodoWrite. Nil keeps TodoWrite persistence-only.
	OnTodosUpdated   func(ctx context.Context, sessionID, workDir string, todos []tool.TodoItem)
	QueuedPrompts    func() []string
	RecordFileChange func(ctx context.Context, uctx *tool.UseContext, change tool.FileChange)
	// CaptureCheckpoint records turn-start file content for code-only rewind.
	CaptureCheckpoint func(path string, content *string)
	// UncaptureCheckpoint removes a path from the active turn when a later
	// fingerprint net-diff against the turn baseline shows no remaining change.
	UncaptureCheckpoint func(path string)
	// ListCheckpointPaths returns relative paths captured for the active turn.
	ListCheckpointPaths func() []string
	// FingerprintBaseline returns the active turn's workdir fingerprint, or nil
	// when no turn baseline is available (falls back to per-invoke snapshots).
	FingerprintBaseline func() map[string]tool.FileFingerprint
	// CompactMessages is invoked mid-run when estimated context reaches MaxContextTokens (100%).
	// It must return a shorter message list. Nil disables mid-run compaction.
	CompactMessages func(ctx context.Context, messages []sdk.MessageParam) ([]sdk.MessageParam, error)
	// Router optionally adds a semantic fallback to lexical tool selection.
	// Nil keeps selection purely lexical.
	Router *Router
	// Guardrail optionally holds a tool call for escalation before it runs.
	// It is advisory: permissions remain the control that grants access.
	Guardrail ToolGuardrail
}

type Engine struct {
	config Config
}

func NewEngine(config Config) *Engine {
	return &Engine{config: config}
}

func (e *Engine) UpdateConfig(config Config) {
	e.config = config
}

func skillNameFromInput(input json.RawMessage) string {
	var params struct {
		Skill string `json:"skill"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(params.Skill))
}

func orderedSkillRoots(active string, roots []string) []string {
	active = strings.TrimSpace(active)
	out := make([]string, 0, len(roots))
	if active != "" {
		out = append(out, active)
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" || root == active {
			continue
		}
		out = append(out, root)
	}
	return out
}

type RunRequest struct {
	AgentConfig      agent.AgentConfig
	SessionID        string
	Messages         []sdk.MessageParam
	SessionSummary   string
	MemoryContext    []ContextItem
	ProjectKnowledge string
}

type RunResult struct {
	AgentResult agent.AgentResult
	Messages    []sdk.MessageParam
}

func (e *Engine) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	return e.RunWithHistory(ctx, RunRequest{AgentConfig: cfg}).AgentResult
}

func (e *Engine) RunWithHistory(ctx context.Context, req RunRequest) RunResult {
	if e.config.Client == nil && e.config.Model != nil {
		return e.runLegacyModel(ctx, req)
	}
	return e.runMessagesLoop(ctx, req)
}

func (e *Engine) runLegacyModel(ctx context.Context, req RunRequest) RunResult {
	cfg := req.AgentConfig
	messages := append([]sdk.MessageParam(nil), req.Messages...)
	prompt := cfg.Prompt
	prompt, blocked, errText := e.runUserPromptHook(ctx, cfg, prompt)
	// Plan-mode instructions live on the system prompt; never inject into user turns.
	prompt = permission.StripPlanModePrompt(prompt)
	userMsg, modelText := userMessageFromPrompt(prompt, cfg.WorkDir)
	messages = append(messages, userMsg)
	if blocked || errText != "" {
		return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: errText}, Messages: messages}
	}

	response, err := e.config.Model.Send(ctx, ModelRequest{
		Prompt:  modelText,
		WorkDir: cfg.WorkDir,
	})
	if err != nil {
		return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
	}
	if response.Text != "" {
		messages = append(messages, sdk.NewAssistantMessage(sdk.NewTextBlock(response.Text)))
	}
	return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Output: response.Text}, Messages: messages}
}

func (e *Engine) runMessagesLoop(ctx context.Context, runReq RunRequest) RunResult {
	cfg := runReq.AgentConfig
	if e.config.Client == nil {
		return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: "engine has no anthropic client"}}
	}

	messages := append([]sdk.MessageParam(nil), runReq.Messages...)
	prompt := cfg.Prompt
	prompt, blocked, errText := e.runUserPromptHook(ctx, cfg, prompt)
	// Plan-mode instructions live on the system prompt; never inject into user turns.
	// Also strip any historical plan-mode prefix from older sessions / mode switches.
	prompt = permission.StripPlanModePrompt(prompt)
	userMsg, modelText := userMessageFromPrompt(prompt, cfg.WorkDir)
	messages = append(messages, userMsg)
	if blocked || errText != "" {
		return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: errText}, Messages: messages}
	}
	// modelText is the expanded text used for local token estimation.
	prompt = modelText

	turnLimit := cfg.MaxTurns
	if turnLimit <= 0 && !cfg.UnlimitedTurns {
		turnLimit = e.config.MaxTurns
	}
	if turnLimit <= 0 && !cfg.UnlimitedTurns {
		turnLimit = 10000
	}

	allTools := e.selectedTools(cfg.AllowedTools)
	enabledTools := make(map[string]bool)
	// routingAttempted keeps the semantic router to one advisory request per
	// run instead of one per turn.
	routingAttempted := false
	// skillRouteResolved caches the skill routing answer. The prompt is fixed
	// for the whole run, so unlike tool routing this is computed once and reused
	// even when Jev declines (an empty result is itself the answer).
	skillRouteResolved := false
	var skillRoute []SkillInfo
	// skillRouteText is the rendered instructions of the selected skill, loaded
	// into the conversation so the selection cannot be ignored.
	var skillRouteText string
	executor := NewToolExecutorWithPermissions(e.config.Tools, e.config.Hooks, e.config.Permissions).WithGuardrail(e.config.Guardrail)
	builder := ContextBuilder{
		SystemPrompt: e.config.SystemPrompt,
		ProjectRules: e.config.ProjectRules,
		Skills:       e.config.Skills,
		SkillNames:   e.config.SkillNames,
		PlanMode:     e.config.Permissions != nil && e.config.Permissions.Mode() == permission.ModePlan,
	}

	var finalText string
	// The most recently activated skill owns relative resource paths for the
	// following tool calls. This avoids ambiguity when multiple user-level
	// skills all contain references/ or scripts/.
	activeSkillRoot := ""
	isMain := cfg.Role == "" || cfg.Role == agent.AgentRoleMain
	isTask := cfg.Role == agent.AgentRoleTask
	emitProgress := func(kind, toolName, toolInput, output string, isError bool) {
		if !isTask || e.config.OnAgentProgress == nil {
			return
		}
		e.config.OnAgentProgress(tool.AgentProgressEvent{
			Kind:            kind,
			AgentID:         string(cfg.ID),
			ParentAgentID:   string(cfg.ParentID),
			ParentToolUseID: cfg.ParentToolUseID,
			TaskID:          cfg.TaskID,
			Description:     cfg.Description,
			ToolName:        toolName,
			ToolInput:       toolInput,
			Output:          output,
			IsError:         isError,
		})
	}
	for turn := 0; turnLimit <= 0 || turn < turnLimit; turn++ {
		if err := ctx.Err(); err != nil {
			return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
		}
		modelName := cfg.Model
		if modelName == "" {
			modelName = e.config.ModelName
		}
		// Compact schemas each turn: full registry stays available to the
		// executor and ToolSearch, while only core + sticky + live matches
		// are sent to the model.
		tools := SelectToolsForTurn(allTools, cfg.AllowedTools, selectionQuery(prompt, ""), enabledTools)
		// When lexical matching resolved nothing beyond the core set, ask Jev
		// which capability the request is actually describing. This only runs on
		// the miss path, so prompts that name a tool keep their current behavior
		// and pay no extra request.
		//
		// It is attempted at most once per run: a miss that Jev also cannot
		// resolve would otherwise be re-asked on every turn of the loop.
		if e.config.Router != nil && !routingAttempted && len(cfg.AllowedTools) == 0 {
			if misses := routerMisses(allTools, selectionQuery(prompt, ""), enabledTools, tools); len(misses) > 0 {
				routingAttempted = true
				for _, name := range e.config.Router.RouteTools(ctx, prompt, misses) {
					if hiddenFromModel[name] {
						continue
					}
					enabledTools[name] = true
				}
				// Jev declined or fell back: same discovery as ToolSearch —
				// sticky-enable only query hits this turn. A total miss means
				// nothing extra is enabled; FoldedTools keeps the model-driven
				// ToolSearch path without auto-opening MCP servers.
				if !hasNonCoreEnabled(enabledTools) {
					enableToolsFromQuery(allTools, prompt, enabledTools)
				}
				tools = SelectToolsForTurn(allTools, cfg.AllowedTools, selectionQuery(prompt, ""), enabledTools)
			}
		}
		builder.PlanMode = e.config.Permissions != nil && e.config.Permissions.Mode() == permission.ModePlan
		// Ask Jev which skill fits this prompt and narrow the advertised catalog
		// to it. The catalog is otherwise a list the model has to reason about
		// itself, and narrowing it both sharpens the choice and shortens the
		// prompt. A nil result means Jev had no confident match, and the full
		// catalog is advertised as before.
		if !skillRouteResolved {
			skillRoute = e.routedSkills(ctx, prompt)
			skillRouteResolved = true
			// A selected skill is force-loaded into the conversation. Narrowing
			// the catalog alone leaves the model free to ignore the selection,
			// which would make the routing decision worthless. The empty result
			// for a "none" answer is the hand-back: the model decides.
			skillRouteText = e.forceLoadedSkill(ctx, prompt, skillRoute)
		}
		builder.Skills = skillRoute
		builder.ForceSkill = skillRouteText
		// Tell the model what exists beyond this turn's schema list. Without
		// this it cannot know a capability is merely unloaded rather than
		// absent, so it will not think to search for it.
		builder.FoldedTools = foldedToolsSummary(allTools, tools)
		req := builder.Build(BuildRequest{
			Model:            modelName,
			ProjectKnowledge: runReq.ProjectKnowledge,
			MaxTokens:        e.config.MaxTokens,
			WorkDir:          cfg.WorkDir,
			Messages:         messages,
			Tools:            tools,
			Thinking:         e.config.Thinking,
			ThinkingText:     e.config.ThinkingText,
			Effort:           e.config.Effort,
			Stream:           e.config.Stream,
			SessionSummary:   runReq.SessionSummary,
			MemoryContext:    runReq.MemoryContext,
		})
		if isMain {
			req.OnTextDelta = e.config.OnTextDelta
			req.OnThinkingDelta = e.config.OnThinkingDelta
		}

		// Mid-run guard: if composed context already fills the window, compact
		// before Create so the request does not fail with a context-length error.
		if e.config.MaxContextTokens > 0 && e.config.CompactMessages != nil {
			est := builder.EstimateContextTokens(BuildRequest{
				Model:            modelName,
				ProjectKnowledge: runReq.ProjectKnowledge,
				MaxTokens:        e.config.MaxTokens,
				WorkDir:          cfg.WorkDir,
				Messages:         messages,
				Tools:            tools,
				Thinking:         e.config.Thinking,
				ThinkingText:     e.config.ThinkingText,
				Effort:           e.config.Effort,
				Stream:           e.config.Stream,
				SessionSummary:   runReq.SessionSummary,
				MemoryContext:    runReq.MemoryContext,
			})
			if est >= e.config.MaxContextTokens {
				compacted, cerr := e.config.CompactMessages(ctx, messages)
				if cerr != nil {
					return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: fmt.Sprintf("context full (%d/%d tokens) and compaction failed: %v", est, e.config.MaxContextTokens, cerr)}, Messages: messages}
				}
				if len(compacted) > 0 {
					messages = compacted
					// Compaction already materializes durable summary /
					// project-knowledge messages. Do not re-inject the
					// pre-compact ephemeral fields on top of them.
					req = builder.Build(BuildRequest{
						Model:        modelName,
						MaxTokens:    e.config.MaxTokens,
						WorkDir:      cfg.WorkDir,
						Messages:     messages,
						Tools:        tools,
						Thinking:     e.config.Thinking,
						ThinkingText: e.config.ThinkingText,
						Effort:       e.config.Effort,
						Stream:       e.config.Stream,
					})
				}
			}
		}
		message, err := e.config.Client.Create(ctx, req)
		if err != nil {
			return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
		}
		estimatedContextTokens := builder.EstimateContextTokens(BuildRequest{
			Model:            modelName,
			ProjectKnowledge: runReq.ProjectKnowledge,
			MaxTokens:        e.config.MaxTokens,
			WorkDir:          cfg.WorkDir,
			Messages:         messages[:len(messages)-1],
			Tools:            tools,
			Thinking:         e.config.Thinking,
			ThinkingText:     e.config.ThinkingText,
			Effort:           e.config.Effort,
			Stream:           e.config.Stream,
			SessionSummary:   runReq.SessionSummary,
			MemoryContext:    runReq.MemoryContext,
		}) + int64(tokenest.Text(prompt))
		// Report billing usage for main and task/subagent turns. Only the main
		// agent supplies EstimatedContextTokens so occupancy stays tied to the
		// parent context window; task turns send 0 and must not clobber it.
		if e.config.OnUsage != nil {
			usage := Usage{
				InputTokens:              message.Usage.InputTokens,
				OutputTokens:             message.Usage.OutputTokens,
				CacheCreationInputTokens: message.Usage.CacheCreationInputTokens,
				CacheReadInputTokens:     message.Usage.CacheReadInputTokens,
				MaxContextTokens:         e.config.MaxContextTokens,
			}
			if isMain {
				usage.EstimatedContextTokens = estimatedContextTokens
			}
			e.config.OnUsage(usage)
		}
		if message.StopReason == sdk.StopReasonRefusal {
			return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: "model refused request"}, Messages: messages}
		}

		finalText = cpanthropic.TextFromMessage(message)
		messages = append(messages, message.ToParam())
		toolUses := cpanthropic.ToolUseBlocks(message)
		if message.StopReason != sdk.StopReasonToolUse || len(toolUses) == 0 {
			e.runStopHook(ctx, cfg)
			return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Output: finalText}, Messages: messages}
		}

		results := make([]cpanthropic.ToolResult, 0, len(toolUses))
		for _, use := range toolUses {
			if err := ctx.Err(); err != nil {
				return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
			}
			input := cpanthropic.RawInput(use.Input)
			if isMain && e.config.OnToolStart != nil {
				e.config.OnToolStart(use.Name, input, use.ID)
			}
			emitProgress("tool_start", use.Name, string(input), "", false)
			toolResult := executor.Execute(ctx, ToolCall{
				Name:  use.Name,
				Input: input,
			}, ToolEnv{
				UseContext: &tool.UseContext{
					SessionID:      nonEmpty(runReq.SessionID, string(cfg.ID)),
					MessageID:      use.ID,
					WorkDir:        cfg.WorkDir,
					SkillRoots:     orderedSkillRoots(activeSkillRoot, e.config.SkillRoots),
					AgentID:        string(cfg.ID),
					AgentRole:      string(cfg.Role),
					TodoPath:       e.config.TodoPath,
					FastModel:      e.config.FastModelName,
					TextFileSystem: e.config.TextFileSystem,
					Status: func(status string) {
						if e.config.OnStatus != nil {
							e.config.OnStatus(status)
						}
					},
					OnAgentProgress: e.config.OnAgentProgress,
					RecordFileChange: func(changeCtx context.Context, change tool.FileChange) {
						if e.config.RecordFileChange != nil {
							e.config.RecordFileChange(changeCtx, &tool.UseContext{
								SessionID:  nonEmpty(runReq.SessionID, string(cfg.ID)),
								WorkDir:    cfg.WorkDir,
								SkillRoots: orderedSkillRoots(activeSkillRoot, e.config.SkillRoots),
							}, change)
						}
					},
					CaptureCheckpoint:   e.config.CaptureCheckpoint,
					UncaptureCheckpoint: e.config.UncaptureCheckpoint,
					ListCheckpointPaths: e.config.ListCheckpointPaths,
					FingerprintBaseline: func() map[string]tool.FileFingerprint {
						if e.config.FingerprintBaseline == nil {
							return nil
						}
						return e.config.FingerprintBaseline()
					}(),
					AskUser:           e.config.OnAskUser,
					AskUserAutoSelect: e.config.AskUserAutoSelect,
					OnTodosUpdated: func(todoCtx context.Context, todos []tool.TodoItem) {
						if e.config.OnTodosUpdated == nil {
							return
						}
						e.config.OnTodosUpdated(todoCtx, nonEmpty(runReq.SessionID, string(cfg.ID)), cfg.WorkDir, todos)
					},
				},
			})
			if err := ctx.Err(); err != nil {
				return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
			}
			if use.Name == tool.SkillToolName && !toolResult.IsError {
				if name := skillNameFromInput(input); name != "" {
					if root := e.config.SkillRootsByName[name]; root != "" {
						activeSkillRoot = root
					}
					// Bundled computer-use skill documents a non-core tool; sticky-
					// enable it so subsequent turns include the ComputerUse schema.
					if name == "computer-use" {
						enabledTools[tool.ComputerUseToolName] = true
					}
				}
			}
			// Keep tools the model actually used (and ToolSearch hits) sticky so
			// subsequent turns retain their schemas without re-sending the full
			// MCP inventory.
			if !toolResult.IsError {
				enabledTools[use.Name] = true
				if use.Name == tool.ToolSearchToolName {
					enableToolsFromSearch(allTools, input, enabledTools)
				}
			}
			apiResult := toolResultToAPI(use.ID, toolResult)
			text := apiResult.Text
			isError := apiResult.IsError
			if isMain && e.config.OnToolDone != nil {
				// UI gets caption text only — never dump base64 image payloads.
				e.config.OnToolDone(use.Name, text, isError, use.ID)
			}
			emitProgress("tool_done", use.Name, "", text, isError)
			// Tool failures/timeouts (and Task tool errors) must not abort the agent
			// loop — especially Task sub-agents, which should keep running and recover
			// from is_error tool_results. Only context cancel / model errors stop the run.
			results = append(results, apiResult)
		}
		messages = append(messages, sdk.NewUserMessage(cpanthropic.ToolResultBlocks(results)...))
		if isMain && e.config.QueuedPrompts != nil {
			for _, queued := range e.config.QueuedPrompts() {
				queued = strings.TrimSpace(queued)
				if queued == "" {
					continue
				}
				// Plan-mode instructions are on the system prompt only.
				queued = permission.StripPlanModePrompt(queued)
				msg, _ := userMessageFromPrompt(queued, cfg.WorkDir)
				messages = append(messages, msg)
			}
		}
	}

	e.runStopHook(ctx, cfg)
	if finalText != "" {
		return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Output: finalText}, Messages: messages}
	}
	return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: fmt.Sprintf("max turns reached: %d", turnLimit)}, Messages: messages}
}

// userMessageFromPrompt expands @path attachments (inlining text files and
// converting images to multimodal blocks) into a user MessageParam.
func userMessageFromPrompt(prompt, workDir string) (sdk.MessageParam, string) {
	expanded := attach.Expand(prompt, workDir)
	return attach.UserMessage(expanded), expanded.Text
}

// toolResultToAPI maps a tool ContentBlock into an API tool_result payload.
// Image results carry base64 data for multimodal tool_result content.
func toolResultToAPI(toolUseID string, toolResult ToolResult) cpanthropic.ToolResult {
	out := cpanthropic.ToolResult{
		ToolUseID: toolUseID,
		IsError:   true,
	}
	if toolResult.Content == nil {
		return out
	}
	out.Text = toolResult.Content.Text
	out.IsError = toolResult.IsError || toolResult.Content.IsError
	if toolResult.Content.Type == "image" && toolResult.Content.Data != "" {
		out.ImageMimeType = toolResult.Content.MimeType
		out.ImageData = toolResult.Content.Data
	}
	return out
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (e *Engine) selectedTools(allowed []string) []tool.Tool {
	if e.config.Tools == nil {
		return nil
	}
	if allowed == nil {
		return e.config.Tools.All()
	}
	return e.config.Tools.Filter(allowed)
}

// routedSkills returns the skill catalog to advertise for this prompt.
//
// When Jev routing is configured it asks which single skill fits and returns
// only that one, which makes the model's choice unambiguous and keeps the
// prompt smaller. Anything else — Jev off, no skills, no confident match, or an
// explicit "none" — returns the full catalog unchanged, so the model still
// selects on its own.
func (e *Engine) routedSkills(ctx context.Context, prompt string) []SkillInfo {
	if e.config.Router == nil || len(e.config.Skills) == 0 {
		return e.config.Skills
	}
	chosen := e.config.Router.RouteSkills(ctx, prompt, e.config.Skills)
	if chosen == "" {
		return e.config.Skills
	}
	for _, info := range e.config.Skills {
		if strings.EqualFold(strings.TrimSpace(info.Name), chosen) {
			return []SkillInfo{info}
		}
	}
	return e.config.Skills
}

// forceLoadedSkill renders the skill Jev selected for this run, or "" when none
// was selected.
//
// Selecting a skill is a decision, and a decision the model can ignore is not
// worth a request: advertising the skill only narrows the catalog, leaving the
// model free to skip it. So the chosen skill's instructions are loaded into the
// conversation directly, the same text the Skill tool would have returned.
//
// Returns "" for the "none" downgrade, which is the explicit hand-back: no
// skill fits confidently, so the model decides for itself with the full catalog.
func (e *Engine) forceLoadedSkill(ctx context.Context, prompt string, skills []SkillInfo) string {
	if e.config.Router == nil || e.config.SkillRegistry == nil || len(skills) != 1 {
		return ""
	}
	name := strings.TrimSpace(skills[0].Name)
	if name == "" || strings.EqualFold(name, RouterNoneSkill) {
		return ""
	}
	def, ok := e.config.SkillRegistry.Find(name)
	if !ok {
		return ""
	}
	return tool.RenderSkillActivation(def, "")
}

func (e *Engine) runUserPromptHook(ctx context.Context, cfg agent.AgentConfig, prompt string) (string, bool, string) {
	if e.config.Hooks == nil {
		return prompt, false, ""
	}
	result, err := e.config.Hooks.Run(ctx, hook.Event{
		Name:    hook.EventUserPromptSubmit,
		AgentID: string(cfg.ID),
		WorkDir: cfg.WorkDir,
		Prompt:  prompt,
	})
	if err != nil {
		return prompt, false, err.Error()
	}
	if result.Decision == hook.DecisionBlock {
		return prompt, true, result.Message
	}
	if result.ModifiedPrompt != "" {
		prompt = result.ModifiedPrompt
	}
	return prompt, false, ""
}

func (e *Engine) runStopHook(ctx context.Context, cfg agent.AgentConfig) {
	if e.config.Hooks == nil {
		return
	}
	_, _ = e.config.Hooks.Run(ctx, hook.Event{
		Name:    hook.EventStop,
		AgentID: string(cfg.ID),
		WorkDir: cfg.WorkDir,
	})
}
