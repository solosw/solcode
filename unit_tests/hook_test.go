package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/solosw/solcode/internal/hook"
)

func hookResultCommand(t *testing.T, result string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "hook.cmd")
		if err := os.WriteFile(path, []byte("@echo off\r\necho "+result+"\r\n"), 0o755); err != nil {
			t.Fatalf("write hook script: %v", err)
		}
		return path
	}
	path := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nprintf '"+result+"'\n"), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}
	return "bash \"" + filepath.ToSlash(path) + "\""
}

func hookMarkerCommand(t *testing.T, path string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		script := filepath.Join(dir, "marker.cmd")
		if err := os.WriteFile(script, []byte("@echo off\r\ntype nul > \""+path+"\"\r\n"), 0o755); err != nil {
			t.Fatalf("write marker script: %v", err)
		}
		return script
	}
	script := filepath.Join(dir, "marker.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\ntouch \""+path+"\"\n"), 0o755); err != nil {
		t.Fatalf("write marker script: %v", err)
	}
	return "bash \"" + filepath.ToSlash(script) + "\""
}

func TestHookRuntime_PreToolUseCommandCanModifyToolInput(t *testing.T) {
	runtime := hook.NewRuntime(hook.Config{
		Events: map[hook.EventName][]hook.MatcherConfig{
			hook.EventPreToolUse: {
				{
					Matcher: "Bash",
					Hooks: []hook.CommandConfig{
						{
							Type:      "command",
							Command:   hookResultCommand(t, `{"decision":"modify","modified_input":{"command":"rtk git status"},"message":"rewritten through rtk"}`),
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
							Command:   hookResultCommand(t, `{"decision":"block","message":"blocked by policy"}`),
							TimeoutMS: 5000,
						},
						{
							Type:      "command",
							Command:   hookMarkerCommand(t, markerPath),
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
