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

type Config struct {
	Model            Model
	Client           *cpanthropic.Client
	Hooks            *hook.Runtime
	Tools            *tool.Registry
	Permissions      *permission.Service
	ModelName        string
	FastModelName    string
	MaxContextTokens int64
	MaxTokens        int64
	SystemPrompt     string
	SkillNames       []string
	MaxTurns         int
	Stream           bool
	Thinking         bool
	ThinkingText     bool
	Effort           string
	TodoPath         string
	OnTextDelta      func(string)
	OnThinkingDelta  func(string)
	OnToolStart      func(name string, input json.RawMessage)
	OnToolDone       func(name string, output string, isError bool)
	OnUsage          func(Usage)
	OnAskUser        func(ctx context.Context, params tool.AskUserParams) (map[string]string, error)
	QueuedPrompts    func() []string
	RecordFileChange func(ctx context.Context, uctx *tool.UseContext, change tool.FileChange)
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

	tools := e.selectedTools(cfg.AllowedTools)
	executor := NewToolExecutorWithPermissions(e.config.Tools, e.config.Hooks, e.config.Permissions)
	builder := ContextBuilder{
		SystemPrompt: e.config.SystemPrompt,
		SkillNames:   e.config.SkillNames,
	}

	var finalText string
	isMain := cfg.Role == "" || cfg.Role == agent.AgentRoleMain
	for turn := 0; turnLimit <= 0 || turn < turnLimit; turn++ {
		if err := ctx.Err(); err != nil {
			return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
		}
		modelName := cfg.Model
		if modelName == "" {
			modelName = e.config.ModelName
		}
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
		if isMain && e.config.OnUsage != nil {
			e.config.OnUsage(Usage{
				EstimatedContextTokens:   estimatedContextTokens,
				InputTokens:              message.Usage.InputTokens,
				OutputTokens:             message.Usage.OutputTokens,
				CacheCreationInputTokens: message.Usage.CacheCreationInputTokens,
				CacheReadInputTokens:     message.Usage.CacheReadInputTokens,
				MaxContextTokens:         e.config.MaxContextTokens,
			})
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
				e.config.OnToolStart(use.Name, input)
			}
			toolResult := executor.Execute(ctx, ToolCall{
				Name:  use.Name,
				Input: input,
			}, ToolEnv{
				UseContext: &tool.UseContext{
					SessionID: nonEmpty(runReq.SessionID, string(cfg.ID)),
					MessageID: use.ID,
					WorkDir:   cfg.WorkDir,
					AgentID:   string(cfg.ID),
					TodoPath:  e.config.TodoPath,
					FastModel: e.config.FastModelName,
					RecordFileChange: func(changeCtx context.Context, change tool.FileChange) {
						if e.config.RecordFileChange != nil {
							e.config.RecordFileChange(changeCtx, &tool.UseContext{SessionID: nonEmpty(runReq.SessionID, string(cfg.ID)), WorkDir: cfg.WorkDir}, change)
						}
					},
					AskUser: e.config.OnAskUser,
				},
			})
			if err := ctx.Err(); err != nil {
				return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
			}
			apiResult := toolResultToAPI(use.ID, toolResult)
			text := apiResult.Text
			isError := apiResult.IsError
			if isMain && e.config.OnToolDone != nil {
				// UI gets caption text only — never dump base64 image payloads.
				e.config.OnToolDone(use.Name, text, isError)
			}
			if isError {
				if cfg.Role == agent.AgentRoleTask {
					results = append(results, apiResult)
					messages = append(messages, sdk.NewUserMessage(cpanthropic.ToolResultBlocks(results)...))
					return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: fmt.Sprintf("tool %s failed: %s", use.Name, text)}, Messages: messages}
				}
				if use.Name == tool.TaskToolName {
					results = append(results, apiResult)
					messages = append(messages, sdk.NewUserMessage(cpanthropic.ToolResultBlocks(results)...))
					return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: fmt.Sprintf("task failed: %s", text)}, Messages: messages}
				}
			}
			results = append(results, apiResult)
		}
		messages = append(messages, sdk.NewUserMessage(cpanthropic.ToolResultBlocks(results)...))
		if isMain && e.config.QueuedPrompts != nil {
			for _, queued := range e.config.QueuedPrompts() {
				queued = strings.TrimSpace(queued)
				if queued == "" {
					continue
				}
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
