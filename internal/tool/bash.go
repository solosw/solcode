package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/executil"
	"github.com/solosw/solcode/internal/sandbox"
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
	// RunInBackground asks the main-agent scheduler to yield immediately.
	// Standalone/nested execution remains synchronous with the same deadline.
	RunInBackground bool `json:"run_in_background,omitempty"`
}

const (
	BashToolName = "Bash"
	// DefaultTimeout is used when the model omits timeout (1 minute).
	DefaultTimeout = 60_000
	// MaxTimeout caps a single Bash invocation / background job lifetime (24h).
	MaxTimeout = 86_400_000
	// AutoWaitThresholdMs: timeouts above this run as a background job and the
	// tool call blocks (waits) until the job finishes or the timeout elapses.
	// The model never sees a separate Wait tool — duration equals Bash timeout.
	AutoWaitThresholdMs = 180_000 // 3 minutes
	MaxOutputLength     = 30_000
)

// autoWaitThresholdMs is the effective threshold (overridable in tests).
var autoWaitThresholdMs = AutoWaitThresholdMs

// bannedCommands are blocked for security.
var bannedCommands = []string{
	"alias", "curl", "curlie", "wget", "axel", "aria2c",
	"nc", "telnet", "lynx", "w3m", "links", "httpie", "xh",
	"http-prompt", "chrome", "firefox", "safari",
}

type bashTool struct {
	BaseTool
	shell         ShellRunner
	sandboxPolicy sandbox.Policy
	jobs          *BackgroundJobs
}

// NewBashTool creates a new bash execution tool.
func NewBashTool() Tool {
	return NewBashToolWithShell(DefaultShell())
}

// NewBashToolWithShell creates a new bash execution tool with a custom ShellRunner.
func NewBashToolWithShell(shell ShellRunner) Tool {
	return &bashTool{shell: shell, jobs: DefaultBackgroundJobs()}
}

// NewBashToolWithSandbox creates a Bash tool that applies policy to each command.
func NewBashToolWithSandbox(policy sandbox.Policy) Tool {
	return &bashTool{shell: DefaultShell(), sandboxPolicy: policy, jobs: DefaultBackgroundJobs()}
}

// NewBashToolWithJobs creates a Bash tool bound to a specific background registry (tests).
func NewBashToolWithJobs(shell ShellRunner, jobs *BackgroundJobs) Tool {
	if shell == nil {
		shell = DefaultShell()
	}
	if jobs == nil {
		jobs = DefaultBackgroundJobs()
	}
	return &bashTool{shell: shell, jobs: jobs}
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
- Timeout in milliseconds (max %d = 24h). Default %d (1m).
- When enabled by the host (default), the main agent yields long-running calls after its configured
  foreground interval (default 10s), allowing independent work to continue. Completion
  and output are delivered automatically; do not poll or restart the command.
- Set run_in_background=true to yield immediately when host scheduling is enabled.
- timeout is the command's hard deadline, NOT the foreground yielding interval.
- Internally, timeouts above %d (3m) use the command registry. No Wait call is needed.
- Do not start work that depends on this command until its completion notification.`,
		bannedStr, MaxOutputLength, MaxTimeout, DefaultTimeout, AutoWaitThresholdMs)
}

func (b *bashTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"run_in_background": map[string]any{
				"type":        "boolean",
				"description": "Yield immediately and receive a completion notification later (main agent, when background scheduling is enabled). The command timeout still applies.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "The bash command to execute",
			},
			"timeout": map[string]any{
				"type": "integer",
				"description": fmt.Sprintf(
					"Command execution deadline in milliseconds (default %d, max %d = 24h). Separate from the host's automatic foreground yielding interval.",
					DefaultTimeout, MaxTimeout,
				),
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

	fields := strings.Fields(params.Command)
	if len(fields) == 0 {
		return ErrorResult("command is required"), nil
	}
	baseCmd := fields[0]
	for _, banned := range bannedCommands {
		if strings.EqualFold(baseCmd, banned) {
			return ErrorResult(fmt.Sprintf("command '%s' is not allowed", baseCmd)), nil
		}
	}

	workDir := ""
	if uctx != nil {
		workDir = uctx.WorkDir
	}
	timeout := time.Duration(params.Timeout) * time.Millisecond

	// Long timeouts (and legacy run_in_background) track the process as a
	// background job and block this tool call for the same duration — Wait is
	// not model-visible; Bash itself suspends until the job finishes.
	if params.Timeout > autoWaitThresholdMs || params.RunInBackground {
		return b.invokeBackgroundAndWait(ctx, params.Command, workDir, timeout)
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdoutBuf, stderrBuf safeBuffer
	exitCode, execErr := b.runCommandTo(execCtx, params.Command, workDir, &stdoutBuf, &stderrBuf)
	return formatCommandResult(stdoutBuf.String(), stderrBuf.String(), exitCode, execErr, execCtx.Err() == context.DeadlineExceeded), nil
}

// invokeBackgroundAndWait starts the command under BackgroundJobs and blocks
// until it finishes. Wait duration equals the job timeout (capped at MaxTimeout).
func (b *bashTool) invokeBackgroundAndWait(ctx context.Context, command, workDir string, timeout time.Duration) (*ContentBlock, error) {
	jobs := b.jobs
	if jobs == nil {
		jobs = DefaultBackgroundJobs()
	}
	if timeout <= 0 {
		timeout = time.Duration(DefaultTimeout) * time.Millisecond
	}
	if timeout > time.Duration(MaxTimeout)*time.Millisecond {
		timeout = time.Duration(MaxTimeout) * time.Millisecond
	}

	id := jobs.StartWithContext(ctx, command, workDir, timeout, func(jobCtx context.Context, stdout, stderr *safeBuffer) (int, error) {
		return b.runCommandTo(jobCtx, command, workDir, stdout, stderr)
	})

	// Bound the wait by the same lifetime as the job; timeout<=0 on Wait would
	// also work under a child ctx, but an explicit bound matches "wait = timeout".
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snap, err := jobs.Wait(waitCtx, id, timeout)
	if err != nil && err != errWaitTimeout && waitCtx.Err() == nil && !isUnknownJob(err) {
		return ErrorResult(err.Error()), nil
	}

	timedOut := err == errWaitTimeout || waitCtx.Err() == context.DeadlineExceeded
	canceled := waitCtx.Err() == context.Canceled
	if (timedOut || canceled) && snap.Status == JobRunning {
		// The wait and execution deadline can race. Do not announce a finished
		// background call while its process is still shutting down, or leave it
		// detached after the executor cancels this invocation's context.
		jobs.Cancel(id)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		final, _ := jobs.Wait(shutdownCtx, id, 0)
		shutdownCancel()
		if final.ID != "" {
			snap = final
		}
	}
	text := formatJobSnapshot(snap)
	if timedOut && snap.Status != JobCompleted {
		text = fmt.Sprintf("Command timed out after %s.\n\n%s", timeout.Round(time.Millisecond), text)
	} else if canceled && snap.Status != JobCompleted {
		text = fmt.Sprintf("Command canceled.\n\n%s", text)
	}
	isErr := snap.Status == JobFailed || snap.Status == JobTimedOut || snap.Status == JobCanceled || snap.Status == JobRunning

	return &ContentBlock{Type: "text", Text: text, IsError: isErr}, nil
}

func formatCommandResult(stdoutRaw, stderrRaw string, exitCode int, execErr error, timedOut bool) *ContentBlock {
	stdout := TruncateOutput(stdoutRaw, MaxOutputLength)
	stderr := TruncateOutput(stderrRaw, MaxOutputLength)

	var result strings.Builder
	if stdout != "" {
		result.WriteString(stdout)
	}

	var errs []string
	if stderr != "" {
		errs = append(errs, stderr)
	}
	if timedOut {
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

	return &ContentBlock{
		Type:    "text",
		Text:    output,
		IsError: exitCode != 0 || execErr != nil || timedOut,
	}
}

// runCommandTo executes a command via the configured ShellRunner, streaming
// output into the provided writers (may be nil).
func (b *bashTool) runCommandTo(ctx context.Context, command, workDir string, stdoutW, stderrW *safeBuffer) (int, error) {
	shell := b.shell
	if shell == nil {
		shell = DefaultShell()
	}
	if stdoutW == nil {
		stdoutW = &safeBuffer{}
	}
	if stderrW == nil {
		stderrW = &safeBuffer{}
	}

	if b.sandboxPolicy.Enabled {
		sandboxInstance, err := sandbox.NewWithPolicy(workDir, b.sandboxPolicy)
		if err != nil {
			return 0, err
		}
		program, args := shellCommand(shell, command)
		result, err := sandboxInstance.Run(ctx, sandbox.Command{
			Program: program,
			Args:    args,
		})
		if result.Stdout != "" {
			_, _ = stdoutW.Write([]byte(result.Stdout))
		}
		if result.Stderr != "" {
			_, _ = stderrW.Write([]byte(result.Stderr))
		}
		return result.ExitCode, err
	}

	cmd := shell.Command(ctx, command)
	cmd.Dir = workDir
	guard := configureCommandCancellation(cmd)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if guard != nil {
		// Close only after WaitChildren so KILL_ON_JOB_CLOSE does not reap
		// still-running children (python training, etc.) when cmd.exe exits.
		defer guard.Close()
	}
	err := executil.Run(ctx, cmd, func() {
		if guard != nil {
			guard.AfterStart(cmd.Process)
		}
	})
	// On Windows, cmd.exe may exit while python/etc. children keep running in
	// the job. Block until the job is empty (or ctx cancels/times out).
	if guard != nil {
		if waitErr := guard.WaitChildren(ctx); waitErr != nil && err == nil {
			err = waitErr
		}
	}
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
			err = nil
		} else if ctx.Err() != nil {
			return exitCode, err
		} else {
			return exitCode, err
		}
	}
	return exitCode, nil
}

func shellCommand(shell ShellRunner, command string) (string, []string) {
	switch shell.(type) {
	case cmdShellRunner:
		return "cmd", []string{"/c", command}
	default:
		return "bash", []string{"-c", command}
	}
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
