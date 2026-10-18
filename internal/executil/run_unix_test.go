//go:build !windows

package executil

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type readyWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *readyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

func TestRunCancelClosesInheritedPipes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 5 & echo ready")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout := &readyWriter{ready: make(chan struct{})}
	cmd.Stdout = stdout
	cmd.Stderr = &bytes.Buffer{}
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() { done <- Run(ctx, cmd, func() { close(started) }) }()
	select {
	case <-started:
		// The default Cancel kills only the shell. The child retains its output
		// handles, so returning here requires closing the owned read ends too.
		t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	case err := <-done:
		t.Fatalf("command failed to start: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("command did not start")
	}
	select {
	case <-stdout.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("no command output")
	}
	time.Sleep(100 * time.Millisecond) // shell has exited; child still owns pipes
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close inherited output pipes")
	}
}

func TestRunWaitsForChildOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "(echo start; sleep 0.6; echo end; echo error >&2) &")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := Run(ctx, cmd, nil); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "start\nend\n" || stderr.String() != "error\n" {
		t.Fatalf("child output truncated: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunPreservesDelayedChildStdin(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "(sleep 0.6; cat) <&0 &")
	input := strings.Repeat("large input for child\n", 20000)
	cmd.Stdin = strings.NewReader(input)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := Run(ctx, cmd, nil); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != input {
		t.Fatalf("child input truncated: got %d bytes, want %d", stdout.Len(), len(input))
	}
}

func TestRunDeadlineAndExitError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "exec sleep 5")
	cmd.Stdout = &bytes.Buffer{}
	if err := Run(ctx, cmd, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	cmd = exec.CommandContext(t.Context(), "sh", "-c", "echo failure >&2; exit 7")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var exitErr *exec.ExitError
	if err := Run(t.Context(), cmd, nil); !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || !strings.Contains(stderr.String(), "failure") {
		t.Fatalf("exit error or output lost: %v / %q", err, stderr.String())
	}
}

func TestRunCancelBlockedStdin(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "(sleep 5) <&0 &")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(strings.Repeat("unread input", 100000))
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() { done <- Run(ctx, cmd, func() { close(started) }) }()
	select {
	case <-started:
		t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	case err := <-done:
		t.Fatalf("command failed to start: %v", err)
	case <-time.After(time.Second):
		t.Fatal("command did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release blocked stdin copying")
	}
}

func TestRunStartFailure(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/nonexistent-solcode-command")
	cmd.Stdout = &bytes.Buffer{}
	if err := Run(t.Context(), cmd, func() { t.Error("afterStart ran on failure") }); err == nil {
		t.Fatal("expected start error")
	}
}
