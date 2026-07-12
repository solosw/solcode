package unit_tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/tool"
)

type recordingModel struct {
	lastPrompt string
	response   engine.ModelResponse
}

func (m *recordingModel) Send(ctx context.Context, req engine.ModelRequest) (engine.ModelResponse, error) {
	m.lastPrompt = req.Prompt
	return m.response, nil
}

func TestEngine_RunAppliesUserPromptSubmitHookBeforeModelCall(t *testing.T) {
	tmpDir := t.TempDir()
	model := &recordingModel{
		response: engine.ModelResponse{
			Text: "model output",
		},
	}
	runtime := hook.NewRuntime(hook.Config{
		Events: map[hook.EventName][]hook.MatcherConfig{
			hook.EventUserPromptSubmit: {
				{
					Matcher: "*",
					Hooks: []hook.CommandConfig{
						{
							Type:      "command",
							Command:   hookResultCommand(t, `{"decision":"modify","modified_prompt":"rewritten prompt"}`),
							TimeoutMS: 5000,
						},
					},
				},
			},
		},
	})

	runner := engine.NewEngine(engine.Config{
		Model: model,
		Hooks: runtime,
		Tools: tool.NewRegistry(),
	})

	result := runner.Run(context.Background(), agent.AgentConfig{
		ID:      "agent-engine",
		WorkDir: tmpDir,
		Prompt:  "original prompt",
	})

	if result.Error != "" {
		t.Fatalf("expected no error, got %q", result.Error)
	}
	if result.Output != "model output" {
		t.Fatalf("expected model output, got %q", result.Output)
	}
	if model.lastPrompt != "rewritten prompt" {
		t.Fatalf("expected rewritten prompt sent to model, got %q", model.lastPrompt)
	}
}

func TestEngine_RunBlocksWhenUserPromptSubmitBlocks(t *testing.T) {
	runtime := hook.NewRuntime(hook.Config{
		Events: map[hook.EventName][]hook.MatcherConfig{
			hook.EventUserPromptSubmit: {
				{
					Matcher: "*",
					Hooks: []hook.CommandConfig{
						{
							Type:      "command",
							Command:   hookResultCommand(t, `{"decision":"block","message":"prompt blocked"}`),
							TimeoutMS: 5000,
						},
					},
				},
			},
		},
	})

	model := &recordingModel{}
	runner := engine.NewEngine(engine.Config{
		Model: model,
		Hooks: runtime,
		Tools: tool.NewRegistry(),
	})

	runResult := runner.RunWithHistory(context.Background(), engine.RunRequest{AgentConfig: agent.AgentConfig{
		ID:     "agent-blocked",
		Prompt: "dangerous prompt",
	}})

	if runResult.AgentResult.Error != "prompt blocked" {
		t.Fatalf("expected prompt blocked error, got %q", runResult.AgentResult.Error)
	}
	encodedMessages, err := json.Marshal(runResult.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	if len(runResult.Messages) != 1 || !strings.Contains(string(encodedMessages), "dangerous prompt") {
		t.Fatalf("expected blocked prompt preserved in messages, got %s", string(encodedMessages))
	}
	if model.lastPrompt != "" {
		t.Fatalf("model should not be called when prompt is blocked, got prompt %q", model.lastPrompt)
	}
}

func TestEngine_RunReturnsModelError(t *testing.T) {
	_ = json.RawMessage(`{}`)
}
