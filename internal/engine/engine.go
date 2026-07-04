package engine

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/codeplus-agent/internal/agent"
	cpanthropic "github.com/solosw/codeplus-agent/internal/anthropic"
	"github.com/solosw/codeplus-agent/internal/hook"
	"github.com/solosw/codeplus-agent/internal/permission"
	"github.com/solosw/codeplus-agent/internal/tool"
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
	MaxContextTokens int64
	MaxTokens        int64
	SystemPrompt     string
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
	AgentConfig    agent.AgentConfig
	SessionID      string
	Messages       []sdk.MessageParam
	SessionSummary string
	MemoryContext  []ContextItem
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
	messages = append(messages, sdk.NewUserMessage(sdk.NewTextBlock(prompt)))
	prompt, blocked, errText := e.runUserPromptHook(ctx, cfg, prompt)
	messages[len(messages)-1] = sdk.NewUserMessage(sdk.NewTextBlock(prompt))
	if blocked || errText != "" {
		return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: errText}, Messages: messages}
	}

	response, err := e.config.Model.Send(ctx, ModelRequest{
		Prompt:  prompt,
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
	messages = append(messages, sdk.NewUserMessage(sdk.NewTextBlock(prompt)))
	prompt, blocked, errText := e.runUserPromptHook(ctx, cfg, prompt)
	messages[len(messages)-1] = sdk.NewUserMessage(sdk.NewTextBlock(prompt))
	if blocked || errText != "" {
		return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: errText}, Messages: messages}
	}

	turnLimit := cfg.MaxTurns
	if turnLimit <= 0 {
		turnLimit = e.config.MaxTurns
	}
	if turnLimit <= 0 {
		turnLimit = 10
	}

	tools := e.selectedTools(cfg.AllowedTools)
	executor := NewToolExecutorWithPermissions(e.config.Tools, e.config.Hooks, e.config.Permissions)
	builder := ContextBuilder{SystemPrompt: e.config.SystemPrompt}

	var finalText string
	for turn := 0; turn < turnLimit; turn++ {
		if err := ctx.Err(); err != nil {
			return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
		}
		req := builder.Build(BuildRequest{
			Model:          e.config.ModelName,
			MaxTokens:      e.config.MaxTokens,
			WorkDir:        cfg.WorkDir,
			Messages:       messages,
			Tools:          tools,
			Thinking:       e.config.Thinking,
			ThinkingText:   e.config.ThinkingText,
			Effort:         e.config.Effort,
			Stream:         e.config.Stream,
			SessionSummary: runReq.SessionSummary,
			MemoryContext:  runReq.MemoryContext,
		})
		req.OnTextDelta = e.config.OnTextDelta
		req.OnThinkingDelta = e.config.OnThinkingDelta

		message, err := e.config.Client.Create(ctx, req)
		if err != nil {
			return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
		}
		if e.config.OnUsage != nil {
			e.config.OnUsage(Usage{
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
			if e.config.OnToolStart != nil {
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
				},
			})
			if err := ctx.Err(); err != nil {
				return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: err.Error()}, Messages: messages}
			}
			text := ""
			isError := true
			if toolResult.Content != nil {
				text = toolResult.Content.Text
				isError = toolResult.IsError || toolResult.Content.IsError
			}
			if e.config.OnToolDone != nil {
				e.config.OnToolDone(use.Name, text, isError)
			}
			results = append(results, cpanthropic.ToolResult{ToolUseID: use.ID, Text: text, IsError: isError})
		}
		messages = append(messages, sdk.NewUserMessage(cpanthropic.ToolResultBlocks(results)...))
	}

	e.runStopHook(ctx, cfg)
	if finalText != "" {
		return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Output: finalText}, Messages: messages}
	}
	return RunResult{AgentResult: agent.AgentResult{AgentID: cfg.ID, Error: fmt.Sprintf("max turns reached: %d", turnLimit)}, Messages: messages}
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
