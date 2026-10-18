//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/solosw/solcode/internal/executil"
)

func (s *Sandbox) confineAndRun(ctx context.Context, cmd Command) (Result, error) {
	args, ok := s.bwrapArgs(cmd)
	if !ok {
		return s.unconfinedRun(ctx, cmd)
	}

	c := exec.CommandContext(ctx, "bwrap", args...)
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
		return Result{}, fmt.Errorf("native sandbox (linux): %w", err)
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
		if result.Stdout == "" && strings.HasPrefix(strings.TrimSpace(result.Stderr), "bwrap:") {
			return s.unconfinedRun(ctx, cmd)
		}
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("native sandbox (linux): %w", err)
}

func (s *Sandbox) bwrapArgs(cmd Command) ([]string, bool) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return nil, false
	}

	args := []string{
		"--unshare-user-try",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-cgroup-try",
		"--new-session",
		"--die-with-parent",
		"--proc", "/proc",
		"--dev", "/dev",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf",
		"--ro-bind-try", "/etc/hosts", "/etc/hosts",
		"--ro-bind-try", "/etc/nsswitch.conf", "/etc/nsswitch.conf",
		"--ro-bind-try", "/etc/ssl", "/etc/ssl",
	}
	if s.policy.Network == "isolated" {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--bind", s.workDir, "/workspace")
	args = append(args, "--chdir", "/workspace")
	for _, p := range s.policy.WritablePaths {
		if p != "" {
			args = append(args, "--bind", p, p)
		}
	}
	for _, p := range s.policy.ReadablePaths {
		if p != "" {
			args = append(args, "--ro-bind", p, p)
		}
	}
	for _, e := range cmd.Env {
		name, value, ok := strings.Cut(e, "=")
		if ok {
			args = append(args, "--setenv", name, value)
		}
	}
	args = append(args, "--", cmd.Program)
	args = append(args, cmd.Args...)
	return args, true
}
