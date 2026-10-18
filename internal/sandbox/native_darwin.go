//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/solosw/solcode/internal/executil"
)

func (s *Sandbox) confineAndRun(ctx context.Context, cmd Command) (Result, error) {
	profile := s.seatbeltProfile()
	args := []string{"-p", profile, "--", cmd.Program}
	args = append(args, cmd.Args...)
	c := exec.CommandContext(ctx, "sandbox-exec", args...)
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
		return Result{}, fmt.Errorf("native sandbox (darwin): %w", err)
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
	return result, fmt.Errorf("native sandbox (darwin): %w", err)
}

func (s *Sandbox) seatbeltProfile() string {
	quoted := func(p string) string {
		return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
	}
	readOnly := []string{
		"/bin", "/usr/bin", "/usr/lib", "/usr/libexec",
		"/System/Library", "/Library/Developer",
		"/private/var/select/sh", "/private/etc/shells",
		"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom",
	}
	deny := []string{
		"/Users", "/Volumes", "/Applications", "/private/etc",
		"/private/tmp", "/private/var", "/opt", "/usr/local",
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	b.WriteString("(deny network*)\n")
	for _, p := range deny {
		b.WriteString(fmt.Sprintf("(deny file-read* file-write* (subpath %s))\n", quoted(p)))
	}
	b.WriteString(fmt.Sprintf("(allow file-read* file-write* (subpath %s))\n", quoted(s.workDir)))
	for _, p := range readOnly {
		b.WriteString(fmt.Sprintf("(allow file-read* (subpath %s))\n", quoted(p)))
	}
	return b.String()
}
