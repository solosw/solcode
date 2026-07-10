package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/tool"
)

type recordingTool struct {
	lastInput json.RawMessage
}

type blockingTool struct{}

func (r *recordingTool) Name() string                                         { return "Record" }
func (r *recordingTool) Description() string                                  { return "records input" }
func (r *recordingTool) InputSchema() map[string]any                          { return nil }
func (r *recordingTool) IsDestructive(json.RawMessage) bool                   { return false }
func (r *recordingTool) IsReadOnly(json.RawMessage) bool                      { return true }
func (r *recordingTool) IsConcurrencySafe(json.RawMessage) bool               { return true }
func (r *recordingTool) Aliases() []string                                    { return nil }
func (r *recordingTool) ValidateInput(context.Context, json.RawMessage) error { return nil }
func (r *recordingTool) Invoke(ctx context.Context, uctx *tool.UseContext, input json.RawMessage) (*tool.ContentBlock, error) {
	r.lastInput = append(json.RawMessage(nil), input...)
	return tool.Result(string(input)), nil
}

func (b *blockingTool) Name() string                                         { return "Block" }
func (b *blockingTool) Description() string                                  { return "blocks until context ends" }
func (b *blockingTool) InputSchema() map[string]any                          { return nil }
func (b *blockingTool) IsDestructive(json.RawMessage) bool                   { return false }
func (b *blockingTool) IsReadOnly(json.RawMessage) bool                      { return true }
func (b *blockingTool) IsConcurrencySafe(json.RawMessage) bool               { return true }
func (b *blockingTool) Aliases() []string                                    { return nil }
func (b *blockingTool) ValidateInput(context.Context, json.RawMessage) error { return nil }
func (b *blockingTool) Invoke(ctx context.Context, uctx *tool.UseContext, input json.RawMessage) (*tool.ContentBlock, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestToolExecutor_AppliesPreToolUseModifiedInput(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "rewrite.sh")
	script := `#!/usr/bin/env bash
printf '{"decision":"modify","modified_input":{"value":"rewritten"}}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}

	recorder := &recordingTool{}
	registry := tool.NewRegistry()
	registry.Register(recorder)

	hooks := hook.NewRuntime(hook.Config{
		Events: map[hook.EventName][]hook.MatcherConfig{
			hook.EventPreToolUse: {
				{
					Matcher: "Record",
					Hooks: []hook.CommandConfig{
						{
							Type:      "command",
							Command:   "bash " + filepath.ToSlash(scriptPath),
							TimeoutMS: 5000,
						},
					},
				},
			},
		},
	})

	executor := engine.NewToolExecutor(registry, hooks)
	result := executor.Execute(context.Background(), engine.ToolCall{
		Name:  "Record",
		Input: json.RawMessage(`{"value":"original"}`),
	}, engine.ToolEnv{
		UseContext: &tool.UseContext{WorkDir: tmpDir},
	})

	if result.IsError {
		t.Fatalf("expected successful tool execution, got %s", result.Content.Text)
	}
	if string(recorder.lastInput) != `{"value":"rewritten"}` {
		t.Fatalf("expected tool to receive rewritten input, got %s", recorder.lastInput)
	}
	if result.Content.Text != `{"value":"rewritten"}` {
		t.Fatalf("expected rewritten result text, got %q", result.Content.Text)
	}
}

func TestToolExecutor_AppliesPostToolUseModifiedResult(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "annotate.sh")
	script := `#!/usr/bin/env bash
printf '{"decision":"modify","modified_result":{"type":"text","text":"annotated result"}}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}

	registry := tool.NewRegistry()
	registry.Register(&recordingTool{})

	hooks := hook.NewRuntime(hook.Config{
		Events: map[hook.EventName][]hook.MatcherConfig{
			hook.EventPostToolUse: {
				{
					Matcher: "Record",
					Hooks: []hook.CommandConfig{
						{
							Type:      "command",
							Command:   "bash " + filepath.ToSlash(scriptPath),
							TimeoutMS: 5000,
						},
					},
				},
			},
		},
	})

	executor := engine.NewToolExecutor(registry, hooks)
	result := executor.Execute(context.Background(), engine.ToolCall{
		Name:  "Record",
		Input: json.RawMessage(`{"value":"original"}`),
	}, engine.ToolEnv{
		UseContext: &tool.UseContext{WorkDir: tmpDir},
	})

	if result.IsError {
		t.Fatalf("expected successful tool execution, got %s", result.Content.Text)
	}
	if result.Content.Text != "annotated result" {
		t.Fatalf("expected post-hook modified result, got %q", result.Content.Text)
	}
}
