package engine

import (
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	cpanthropic "github.com/solosw/codeplus-agent/internal/anthropic"
	"github.com/solosw/codeplus-agent/internal/tool"
)

type ContextBuilder struct {
	SystemPrompt string
}

type ContextItem struct {
	Title      string
	Content    string
	Source     string
	Importance float64
}

type ContextBudget struct {
	MaxInputTokens       int
	ReserveOutputTokens  int
	RecentTurnsMin       int
	RetrievedMemoryLimit int
}

func (b ContextBuilder) Build(req BuildRequest) cpanthropic.MessageRequest {
	tools := req.Tools
	return cpanthropic.MessageRequest{
		Model:        req.Model,
		MaxTokens:    req.MaxTokens,
		System:       b.systemPrompt(req.WorkDir),
		Messages:     b.withContextMessages(req.Messages, req.SessionSummary, req.MemoryContext),
		Tools:        convertTools(tools),
		Thinking:     req.Thinking,
		ThinkingText: req.ThinkingText,
		Effort:       req.Effort,
		Stream:       req.Stream,
	}
}

// withContextMessages prepends session summary and retrieved memory as an
// ephemeral context user message. They live in the messages stream (after the
// cached system prefix) rather than in the system prompt, so changing the
// summary or retrieved memories does not invalidate the system-prompt cache.
func (b ContextBuilder) withContextMessages(messages []sdk.MessageParam, sessionSummary string, memoryContext []ContextItem) []sdk.MessageParam {
	contextBlock := b.contextBlock(sessionSummary, memoryContext)
	if contextBlock == "" {
		return messages
	}
	summaryMsg := sdk.NewUserMessage(sdk.NewTextBlock(contextBlock))
	assistantAck := sdk.NewAssistantMessage(sdk.NewTextBlock("Understood. I'll keep this context in mind as I work."))
	return append([]sdk.MessageParam{summaryMsg, assistantAck}, messages...)
}

func (b ContextBuilder) contextBlock(sessionSummary string, memoryContext []ContextItem) string {
	var parts []string
	if strings.TrimSpace(sessionSummary) != "" {
		parts = append(parts, "Session summary:\n"+strings.TrimSpace(sessionSummary))
	}
	if len(memoryContext) > 0 {
		parts = append(parts, "Retrieved memory:\n"+formatMemoryContext(memoryContext))
	}
	return strings.Join(parts, "\n\n")
}

type BuildRequest struct {
	Model          string
	MaxTokens      int64
	WorkDir        string
	Messages       []sdk.MessageParam
	Tools          []tool.Tool
	Thinking       bool
	ThinkingText   bool
	Effort         string
	Stream         bool
	SessionSummary string
	MemoryContext  []ContextItem
	ContextBudget  ContextBudget
}

func (b ContextBuilder) systemPrompt(workDir string) string {
	parts := []string{}
	if b.SystemPrompt != "" {
		parts = append(parts, b.SystemPrompt)
	} else {
		parts = append(parts, defaultSystemPrompt())
	}
	if workDir != "" {
		parts = append(parts, "Working directory: "+workDir)
	}
	return strings.Join(parts, "\n\n")
}

func formatMemoryContext(items []ContextItem) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = item.Source
		}
		if title != "" {
			lines = append(lines, "- "+title+": "+content)
		} else {
			lines = append(lines, "- "+content)
		}
	}
	return strings.Join(lines, "\n")
}

func defaultSystemPrompt() string {
	return `You are codeplus-agent, an interactive CLI-based coding agent that helps with software engineering tasks.

# Tone and style
- Be direct and concise. Match the user's energy — terse for quick tasks, fuller for complex design work.
- Lead with the outcome. Your first sentence after finishing should answer "what happened" or "what did you find".
- Use plain prose. Output GitHub-flavored Markdown for code, commands, and file references.
- Don't add preamble ("Sure!", "Let me help you with that"), filler, or excessive hedging.

# Doing tasks
- Take initiative. When you have enough information to act, act — don't re-derive established facts or re-litigate decisions the user already made.
- Use tools to gather information and make changes rather than asking the user for things you can discover yourself.
- After tool results, continue working until the task is complete or you are genuinely blocked on a decision only the user can make.
- Prefer targeted edits over rewrites. Match the style, naming, and idioms of the surrounding code.
- Only make changes directly requested. Don't add features, abstractions, error handling, or refactorings the task didn't call for.

# Working with the user
- For minor choices (naming, formatting, sensible defaults), pick a reasonable option and note it instead of asking.
- For scope changes or hard-to-reverse actions (deleting files, external calls), confirm first.
- Report outcomes faithfully: if tests fail, say so with the output; if a step was skipped, say that. Don't claim success you didn't verify.`
}

func convertTools(tools []tool.Tool) []sdk.ToolUnionParam {
	out := make([]sdk.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, cpanthropic.ToolToSDK(t))
	}
	return out
}
