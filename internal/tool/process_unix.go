//go:build !windows

package tool

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureCommandCancellation puts the shell and all of its children in a
// separate process group. Canceling the tool context then terminates the whole
// group rather than merely the shell process.
func configureCommandCancellation(cmd *exec.Cmd) {
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

	// A process that has already detached from its shell can retain stdout or
	// stderr. Do not let those inherited descriptors block Ctrl+C indefinitely.
	cmd.WaitDelay = 250 * time.Millisecond
}
