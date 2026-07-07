package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AskUserParams is the input schema for the AskUser tool.
type AskUserParams struct {
	Questions []Question `json:"questions"`
}

// Question represents a single question with multiple choice options.
type Question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header,omitempty"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multi_select,omitempty"`
}

// QuestionOption is a selectable choice for a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// AskUserOutput is the structured output of an AskUser tool invocation.
type AskUserOutput struct {
	Questions []Question        `json:"questions"`
	Answers   map[string]string `json:"answers"`
}

// askUserTool allows the model to ask the user questions and gather responses.
type askUserTool struct {
	BaseTool
	// answers is a channel to receive user answers in interactive mode.
	// In non-interactive mode, returns a placeholder.
	answersCh   chan map[string]string
	timeoutSecs int
}

const (
	AskUserToolName = "AskUser"
	AskUserTimeout  = 300 // 5 minutes
)

// NewAskUserTool creates a new AskUser tool.
// In interactive mode, the TUI layer will wire up answersCh.
func NewAskUserTool() Tool {
	return &askUserTool{
		timeoutSecs: AskUserTimeout,
		answersCh:   make(chan map[string]string),
	}
}

// SetAnswersChannel wires up the answers channel from the TUI layer.
func (t *askUserTool) SetAnswersChannel(ch chan map[string]string) {
	t.answersCh = ch
}

func (t *askUserTool) Name() string      { return AskUserToolName }
func (t *askUserTool) Aliases() []string { return []string{"AskUserQuestion"} }

func (t *askUserTool) Description() string {
	return `Asks the user one or more questions to gather information, clarify requirements,
or offer choices. Use this when you need user input to make decisions.

- Questions are shown as interactive dialogs in the TUI
- Users can select an option or type a custom answer
- Use for: gathering preferences, clarifying ambiguity, making decisions
- Each question needs 2-4 options; header labels the question briefly
- In interactive mode, results are returned as answers map`
}

func (t *askUserTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type":        "array",
				"description": "Questions to ask (1-4 at a time)",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{
							"type":        "string",
							"description": "The question to ask (end with ?)",
						},
						"header": map[string]any{
							"type":        "string",
							"description": "Short label/tag for the question (max 12 chars)",
						},
						"options": map[string]any{
							"type":        "array",
							"description": "Available choices (2-4 options)",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"label":       map[string]string{"type": "string", "description": "Short display text (1-5 words)"},
									"description": map[string]string{"type": "string", "description": "What choosing this option means"},
									"preview":     map[string]string{"type": "string", "description": "Optional code/mockup preview shown side-by-side"},
								},
								"required": []string{"label", "description"},
							},
						},
						"multi_select": map[string]any{
							"type":        "boolean",
							"description": "Allow selecting multiple options",
						},
					},
					"required": []string{"question", "options"},
				},
			},
		},
		"required": []string{"questions"},
	}
}

func (t *askUserTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params AskUserParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if len(params.Questions) == 0 {
		return ErrorResult("at least one question is required"), nil
	}
	if len(params.Questions) > 4 {
		return ErrorResult("at most 4 questions allowed per AskUser call"), nil
	}

	// Validate each question
	for i, q := range params.Questions {
		if q.Question == "" {
			return ErrorResult(fmt.Sprintf("question %d has empty question text", i+1)), nil
		}
		if len(q.Options) < 2 {
			return ErrorResult(fmt.Sprintf("question %d needs at least 2 options", i+1)), nil
		}
		if len(q.Options) > 4 {
			return ErrorResult(fmt.Sprintf("question %d has too many options (max 4)", i+1)), nil
		}
		for j, opt := range q.Options {
			if opt.Label == "" {
				return ErrorResult(fmt.Sprintf("question %d option %d has empty label", i+1, j+1)), nil
			}
		}
		if len(q.Header) > 12 {
			return ErrorResult(fmt.Sprintf("question %d header exceeds 12 characters: %q", i+1, q.Header)), nil
		}
	}

	// In interactive mode, ask the TUI and wait for answers.
	if uctx != nil && uctx.AskUser != nil {
		answers, err := uctx.AskUser(ctx, params)
		if err != nil {
			return ErrorResult("AskUser failed: " + err.Error()), nil
		}
		return buildStructuredResult(params.Questions, answers), nil
	}

	// Try to get answers from the channel (legacy interactive mode)
	select {
	case answers := <-t.answersCh:
		return buildStructuredResult(params.Questions, answers), nil
	case <-ctx.Done():
		return ErrorResult("AskUser cancelled: " + ctx.Err().Error()), nil
	default:
		// No answer yet — return the questions summary for TUI to render
		return &ContentBlock{
			Type: "ask_user",
			Text: buildAskUserSummary(params.Questions),
		}, nil
	}
}

func buildAskUserSummary(questions []Question) string {
	var sb strings.Builder
	sb.WriteString("━━━ QUESTIONS ━━━\n\n")
	for i, q := range questions {
		sb.WriteString(fmt.Sprintf("[%d] %s", i+1, q.Question))
		if q.Header != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", q.Header))
		}
		sb.WriteString("\n")
		for j, opt := range q.Options {
			sb.WriteString(fmt.Sprintf("  %d. %s", j+1, opt.Label))
			if opt.Description != "" {
				sb.WriteString(fmt.Sprintf(" — %s", opt.Description))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("━━━ Please select your answer(s) ━━━")
	return sb.String()
}

func buildStructuredResult(questions []Question, answers map[string]string) *ContentBlock {
	var result AskUserOutput
	result.Questions = questions
	result.Answers = answers

	// Format as readable text
	var sb strings.Builder
	for _, q := range questions {
		ans, ok := answers[q.Question]
		if ok {
			sb.WriteString(fmt.Sprintf("Q: %s\nA: %s\n\n", q.Question, ans))
		}
	}

	b, _ := json.Marshal(result)
	sb.WriteString(fmt.Sprintf("\n[Structured result: %s]", string(b)))

	return &ContentBlock{
		Type: "text",
		Text: sb.String(),
	}
}
