package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ShellRunner knows how to turn a command string into an *exec.Cmd for the current OS.
type ShellRunner interface {
	Command(ctx context.Context, command string) *exec.Cmd
}

// bashShellRunner runs commands via bash -c.
type bashShellRunner struct{}

func (bashShellRunner) Command(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "bash", "-c", command)
}

// cmdShellRunner runs commands via cmd /c (Windows).
type cmdShellRunner struct{}

func (cmdShellRunner) Command(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/c", command)
}

// DefaultShell returns a ShellRunner appropriate for the current OS.
func DefaultShell() ShellRunner {
	if runtime.GOOS == "windows" {
		return cmdShellRunner{}
	}
	return bashShellRunner{}
}

// BashParams is the input schema for the bash tool.
type BashParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

const (
	BashToolName    = "Bash"
	DefaultTimeout  = 60_000  // 1 minute in milliseconds
	MaxTimeout      = 600_000 // 10 minutes in milliseconds
	MaxOutputLength = 30_000
)

// bannedCommands are blocked for security.
var bannedCommands = []string{
	"alias", "curl", "curlie", "wget", "axel", "aria2c",
	"nc", "telnet", "lynx", "w3m", "links", "httpie", "xh",
	"http-prompt", "chrome", "firefox", "safari",
}

type bashTool struct {
	BaseTool
	shell ShellRunner
}

// NewBashTool creates a new bash execution tool.
func NewBashTool() Tool {
	return NewBashToolWithShell(DefaultShell())
}

// NewBashToolWithShell creates a new bash execution tool with a custom ShellRunner.
func NewBashToolWithShell(shell ShellRunner) Tool {
	return &bashTool{shell: shell}
}

func (b *bashTool) Name() string                             { return BashToolName }
func (b *bashTool) IsDestructive(_ json.RawMessage) bool     { return true }
func (b *bashTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (b *bashTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (b *bashTool) Description() string {
	bannedStr := strings.Join(bannedCommands, ", ")
	return fmt.Sprintf(`Executes a given bash command with optional timeout.
Security: some commands are banned: %s.
Output is truncated at %d characters.
- Use ';' or '&&' to chain commands, do NOT use newlines.
- Avoid find/grep/cat/head/tail — use Glob, Grep, and View tools instead.
- Timeout in milliseconds (max %d).`, bannedStr, MaxOutputLength, MaxTimeout)
}

func (b *bashTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The bash command to execute",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Optional timeout in milliseconds (max 600000)",
			},
		},
		"required": []string{"command"},
	}
}

func (b *bashTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params BashParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if params.Command == "" {
		return ErrorResult("command is required"), nil
	}

	if params.Timeout <= 0 {
		params.Timeout = DefaultTimeout
	}
	if params.Timeout > MaxTimeout {
		params.Timeout = MaxTimeout
	}

	baseCmd := strings.Fields(params.Command)[0]
	for _, banned := range bannedCommands {
		if strings.EqualFold(baseCmd, banned) {
			return ErrorResult(fmt.Sprintf("command '%s' is not allowed", baseCmd)), nil
		}
	}

	timeout := time.Duration(params.Timeout) * time.Millisecond
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startTime := time.Now()
	stdout, stderr, exitCode, execErr := b.runCommand(execCtx, params.Command, uctx.WorkDir)
	elapsed := time.Since(startTime)

	stdout = TruncateOutput(stdout, MaxOutputLength)
	stderr = TruncateOutput(stderr, MaxOutputLength)

	var result strings.Builder
	if stdout != "" {
		result.WriteString(stdout)
	}

	var errs []string
	if stderr != "" {
		errs = append(errs, stderr)
	}
	if execCtx.Err() == context.DeadlineExceeded {
		errs = append(errs, "Command timed out")
	} else if execErr != nil {
		errs = append(errs, execErr.Error())
	} else if exitCode != 0 {
		errs = append(errs, fmt.Sprintf("Exit code %d", exitCode))
	}

	if len(errs) > 0 {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString(strings.Join(errs, "\n"))
	}

	output := result.String()
	if output == "" {
		output = "no output"
	}

	_ = elapsed // duration tracked but returned implicitly in output

	return &ContentBlock{
		Type:    "text",
		Text:    output,
		IsError: exitCode != 0 || execErr != nil,
	}, nil
}

// runCommand executes a command via the configured ShellRunner.
func (b *bashTool) runCommand(ctx context.Context, command, workDir string) (string, string, int, error) {
	shell := b.shell
	if shell == nil {
		shell = DefaultShell()
	}
	cmd := shell.Command(ctx, command)
	cmd.Dir = workDir
	configureCommandCancellation(cmd)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return stdout.String(), stderr.String(), exitCode, err
		}
	}

	return stdout.String(), stderr.String(), exitCode, nil
}

func TruncateOutput(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}
	half := maxLen / 2
	start := content[:half]
	end := content[len(content)-half:]
	truncatedLines := strings.Count(content[half:len(content)-half], "\n")
	return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, truncatedLines, end)
}
