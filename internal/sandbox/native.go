// Package sandbox provides OS-native command sandbox implementations.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/executil"
)

// Policy controls how strictly the sandbox confines a command.
type Policy struct {
	Enabled       bool     `json:"enabled,omitempty"`
	Network       string   `json:"network,omitempty"`
	WritablePaths []string `json:"writable_paths,omitempty"`
	ReadablePaths []string `json:"readable_paths,omitempty"`
}

// Command describes a program to execute inside the sandbox.
type Command struct {
	Program string
	Args    []string
	Env     []string
	Stdin   string
	Stdout  io.Writer
	Stderr  io.Writer
}

// Result is the captured result of a command invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	PID      int
}

// Sandbox confines commands to a workspace using OS-native mechanisms.
type Sandbox struct {
	workDir string
	policy  Policy
}

// New creates a native sandbox rooted at workDir with the default policy.
func New(workDir string) (*Sandbox, error) {
	return NewWithPolicy(workDir, Policy{})
}

// NewWithPolicy creates a native sandbox rooted at workDir with p.
func NewWithPolicy(workDir string, p Policy) (*Sandbox, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("native sandbox: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("native sandbox: create workspace: %w", err)
	}
	return &Sandbox{workDir: abs, policy: p}, nil
}

// Run executes cmd in a sandboxed child process when the policy is enabled.
func (s *Sandbox) Run(ctx context.Context, cmd Command) (Result, error) {
	if !s.policy.Enabled {
		return s.unconfinedRun(ctx, cmd)
	}
	return s.confineAndRun(ctx, cmd)
}

type cmdOutput struct {
	stdout strings.Builder
	stderr strings.Builder
}

func (co *cmdOutput) configure(c *exec.Cmd, cmd Command) {
	stdout := io.Writer(&co.stdout)
	stderr := io.Writer(&co.stderr)
	if cmd.Stdout != nil {
		stdout = io.MultiWriter(stdout, cmd.Stdout)
	}
	if cmd.Stderr != nil {
		stderr = io.MultiWriter(stderr, cmd.Stderr)
	}
	c.Stdout = stdout
	c.Stderr = stderr
}

func (co *cmdOutput) result(pid int) Result {
	return Result{
		Stdout: co.stdout.String(),
		Stderr: co.stderr.String(),
		PID:    pid,
	}
}

func resultForCommand(co *cmdOutput, cmd *exec.Cmd) Result {
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	return co.result(pid)
}

func (s *Sandbox) unconfinedRun(ctx context.Context, cmd Command) (Result, error) {
	c := exec.CommandContext(ctx, cmd.Program, cmd.Args...)
	c.Dir = s.workDir
	if cmd.Env != nil {
		c.Env = cmd.Env
	}
	if cmd.Stdin != "" {
		c.Stdin = strings.NewReader(cmd.Stdin)
	}

	var co cmdOutput
	co.configure(c, cmd)
	guard := configureCommandCancellation(c)

	if guard != nil {
		defer guard.Close()
	}
	err := executil.Run(ctx, c, func() {
		if guard != nil {
			guard.AfterStart(c.Process)
		}
	})
	if c.Process == nil {
		return Result{}, fmt.Errorf("native sandbox (unconfined): %w", err)
	}
	if guard != nil {
		if waitErr := guard.WaitChildren(ctx); waitErr != nil && err == nil {
			err = waitErr
		}
	}
	result := resultForCommand(&co, c)
	if err == nil {
		return result, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("native sandbox (unconfined): %w", err)
}
