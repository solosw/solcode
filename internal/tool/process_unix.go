//go:build !windows

package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processGuard interface {
	AfterStart(proc *os.Process)
	WaitChildren(ctx context.Context) error
	Close()
}

type unixProcessGuard struct{}

func (unixProcessGuard) AfterStart(*os.Process)             {}
func (unixProcessGuard) WaitChildren(context.Context) error { return nil }
func (unixProcessGuard) Close()                             {}

// configureCommandCancellation puts the shell and all of its children in a
// separate process group. Canceling the tool context then terminates the whole
// group rather than merely the shell process.
// Output pipe lifetime is managed separately by executil.Run.
func configureCommandCancellation(cmd *exec.Cmd) processGuard {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	return unixProcessGuard{}
}
