//go:build windows

package tool

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// configureCommandCancellation terminates the shell and every child process.
// cmd.exe otherwise exits while a command it launched keeps running, causing
// Ctrl+C to appear to wait for the original command to finish.
func configureCommandCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		pid := cmd.Process.Pid

		// Kill the direct process synchronously so Cmd.Wait can return right
		// away. taskkill removes children in the background; waiting for it here
		// made Ctrl+C depend on taskkill's process-startup latency.
		go func() {
			_ = exec.Command("taskkill", "/PID", fmt.Sprint(pid), "/T", "/F").Run()
		}()
		return cmd.Process.Kill()
	}

	// Do not wait indefinitely for output handles inherited by a process which
	// has escaped the tree before it could be terminated.
	cmd.WaitDelay = 250 * time.Millisecond
}
