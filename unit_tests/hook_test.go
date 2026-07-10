package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/hook"
)

func TestHookRuntime_PreToolUseCommandCanModifyToolInput(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "rewrite.sh")
	script := `#!/usr/bin/env bash
printf '{"decision":"modify","modified_input":{"command":"rtk git status"},"message":"rewritten through rtk"}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}

	runtime := hook.NewRuntime(hook.Config{
		Events: map[hook.EventName][]hook.MatcherConfig{
			hook.EventPreToolUse: {
				{
					Matcher: "Bash",
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

	originalInput := json.RawMessage(`{"command":"git status"}`)
	result, err := runtime.Run(context.Background(), hook.Event{
		Name:      hook.EventPreToolUse,
		ToolName:  "Bash",
		ToolInput: originalInput,
	})
	if err != nil {
		t.Fatalf("run hook: %v", err)
	}

	if result.Decision != hook.DecisionModify {
		t.Fatalf("expected modify decision, got %q", result.Decision)
	}
	if string(result.ModifiedInput) != `{"command":"rtk git status"}` {
		t.Fatalf("expected rewritten input, got %s", result.ModifiedInput)
	}
	if result.Message != "rewritten through rtk" {
		t.Fatalf("expected hook message, got %q", result.Message)
	}
}

func TestHookRuntime_CommandFailOpenAllowsProcessing(t *testing.T) {
	runtime := hook.NewRuntime(hook.Config{
		Events: map[hook.EventName][]hook.MatcherConfig{
			hook.EventPreToolUse: {
				{
					Matcher: "Bash",
					Hooks: []hook.CommandConfig{
						{
							Type:      "command",
							Command:   "exit 42",
							TimeoutMS: 5000,
							FailMode:  "open",
						},
					},
				},
			},
		},
	})

	result, err := runtime.Run(context.Background(), hook.Event{
		Name:      hook.EventPreToolUse,
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"git status"}`),
	})
	if err != nil {
		t.Fatalf("fail-open hook should not return error: %v", err)
	}
	if result.Decision != hook.DecisionAllow {
		t.Fatalf("expected allow decision, got %q", result.Decision)
	}
}

func TestHookRuntime_BlockStopsLaterHooks(t *testing.T) {
	tmpDir := t.TempDir()
	markerPath := filepath.Join(tmpDir, "later-ran")

	runtime := hook.NewRuntime(hook.Config{
		Events: map[hook.EventName][]hook.MatcherConfig{
			hook.EventPreToolUse: {
				{
					Matcher: "Bash",
					Hooks: []hook.CommandConfig{
						{
							Type:      "command",
							Command:   `printf '{"decision":"block","message":"blocked by policy"}'`,
							TimeoutMS: 5000,
						},
						{
							Type:      "command",
							Command:   "touch " + filepath.ToSlash(markerPath),
							TimeoutMS: 5000,
						},
					},
				},
			},
		},
	})

	result, err := runtime.Run(context.Background(), hook.Event{
		Name:      hook.EventPreToolUse,
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"rm -rf /tmp/example"}`),
	})
	if err != nil {
		t.Fatalf("run hook: %v", err)
	}
	if result.Decision != hook.DecisionBlock {
		t.Fatalf("expected block decision, got %q", result.Decision)
	}
	if result.Message != "blocked by policy" {
		t.Fatalf("expected block message, got %q", result.Message)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("later hook should not run after block")
	}
}
