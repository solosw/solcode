package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type EventName string

const (
	EventUserPromptSubmit EventName = "UserPromptSubmit"
	EventPreToolUse       EventName = "PreToolUse"
	EventPostToolUse      EventName = "PostToolUse"
	EventNotification     EventName = "Notification"
	EventStop             EventName = "Stop"
)

type Decision string

const (
	DecisionAllow  Decision = "allow"
	DecisionModify Decision = "modify"
	DecisionBlock  Decision = "block"
)

type Config struct {
	Events map[EventName][]MatcherConfig `json:"events,omitempty"`
}

type MatcherConfig struct {
	Matcher string          `json:"matcher,omitempty"`
	Hooks   []CommandConfig `json:"hooks,omitempty"`
}

type CommandConfig struct {
	// Type is "command" (default) or "builtin".
	Type string `json:"type,omitempty"`
	// Command is the shell command for type=command.
	Command string `json:"command,omitempty"`
	// Name selects a built-in hook when type=builtin
	// (e.g. "compress_tool_result").
	Name      string `json:"name,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	FailMode  string `json:"fail_mode,omitempty"`
}

type Event struct {
	Name       EventName       `json:"event"`
	SessionID  string          `json:"session_id,omitempty"`
	MessageID  string          `json:"message_id,omitempty"`
	AgentID    string          `json:"agent_id,omitempty"`
	WorkDir    string          `json:"work_dir,omitempty"`
	Prompt     string          `json:"prompt,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolResult any             `json:"tool_result,omitempty"`
}

type Result struct {
	Decision       Decision        `json:"decision,omitempty"`
	ModifiedPrompt string          `json:"modified_prompt,omitempty"`
	ModifiedInput  json.RawMessage `json:"modified_input,omitempty"`
	ModifiedResult any             `json:"modified_result,omitempty"`
	Message        string          `json:"message,omitempty"`
	SuppressOutput bool            `json:"suppress_output,omitempty"`
}

type Runtime struct {
	config Config
}

func NewRuntime(config Config) *Runtime {
	return &Runtime{config: config}
}

func (r *Runtime) Run(ctx context.Context, event Event) (Result, error) {
	result := Result{Decision: DecisionAllow}
	groups := r.config.Events[event.Name]
	for _, group := range groups {
		if !matches(group.Matcher, event) {
			continue
		}
		for _, hook := range group.Hooks {
			hookType := strings.TrimSpace(hook.Type)
			if hookType == "" {
				hookType = "command"
			}

			var hookResult Result
			var err error
			switch hookType {
			case "command":
				hookResult, err = runCommandHook(ctx, hook, event)
			case "builtin":
				hookResult, err = runBuiltinHook(hook, event)
			default:
				return result, fmt.Errorf("unsupported hook type: %s", hook.Type)
			}
			if err != nil {
				if hook.FailMode == "open" {
					// Keep prior decision; do not abort the tool pipeline.
					continue
				}
				return result, err
			}
			if hookResult.Decision == "" {
				hookResult.Decision = DecisionAllow
			}
			result = hookResult
			if hookResult.ModifiedInput != nil {
				event.ToolInput = hookResult.ModifiedInput
			}
			if hookResult.ModifiedPrompt != "" {
				event.Prompt = hookResult.ModifiedPrompt
			}
			if hookResult.ModifiedResult != nil {
				event.ToolResult = hookResult.ModifiedResult
			}
			if hookResult.Decision == DecisionBlock {
				return result, nil
			}
		}
	}
	return result, nil
}

func matches(matcher string, event Event) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	target := event.ToolName
	if event.Name == EventUserPromptSubmit {
		target = event.Prompt
	}
	for _, part := range strings.Split(matcher, "|") {
		if strings.TrimSpace(part) == target {
			return true
		}
	}
	return false
}

func runCommandHook(ctx context.Context, config CommandConfig, event Event) (Result, error) {
	if config.Command == "" {
		return Result{Decision: DecisionAllow}, errors.New("hook command is required")
	}

	timeout := time.Duration(config.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(event)
	if err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(execCtx, shellBin(), shellCommandArg(), config.Command)
	cmd.Stdin = strings.NewReader(string(payload))
	if event.WorkDir != "" {
		cmd.Dir = event.WorkDir
	}

	output, err := cmd.Output()
	if execCtx.Err() == context.DeadlineExceeded {
		return Result{}, execCtx.Err()
	}
	if err != nil {
		return Result{}, err
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return Result{Decision: DecisionAllow}, nil
	}

	var result Result
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return Result{}, err
	}
	if result.Decision == "" {
		result.Decision = DecisionAllow
	}
	return result, nil
}

func shellBin() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "bash"
}

func shellCommandArg() string {
	if runtime.GOOS == "windows" {
		return "/c"
	}
	return "-c"
}
