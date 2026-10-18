//go:build windows

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsJobGuardHelper(t *testing.T) {
	switch os.Getenv("SOLCODE_WINDOWS_GUARD_HELPER") {
	case "hold":
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	case "exit":
		os.Exit(0)
	}
}

func startWindowsGuardHelper(t *testing.T, g *windowsJobGuard, mode string) <-chan error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsJobGuardHelper$")
	cmd.Env = append(os.Environ(), "SOLCODE_WINDOWS_GUARD_HELPER="+mode)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	g.AfterStart(cmd.Process)
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		done <- cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		// os.Process owns an identity-safe handle, even if guard setup failed.
		_ = cmd.Process.Kill()
		g.Close()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("helper Wait did not finish during cleanup")
		}
	})
	return done
}

func awaitWindowsGuardHelper(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("guard did not terminate the original process")
		return nil
	}
}

func TestWindowsJobGuardCancelAssignedJob(t *testing.T) {
	job, err := createKillOnCloseJob()
	if err != nil {
		t.Skipf("host cannot create kill-on-close Job: %v", err)
	}
	g := &windowsJobGuard{job: job}
	defer g.Close()
	done := startWindowsGuardHelper(t, g, "hold")
	if !g.assigned {
		t.Skip("host does not permit Job assignment")
	}
	if err := g.Cancel(); err != nil {
		t.Fatal(err)
	}
	if g.job != 0 || g.process == 0 || g.closed {
		t.Fatalf("Cancel must close the Job but retain process identity until Close: %+v", g)
	}
	if err := awaitWindowsGuardHelper(t, done); err == nil {
		t.Fatal("Job cancellation did not kill the process")
	}
	if err := g.Cancel(); err != nil {
		t.Fatalf("repeated cancellation: %v", err)
	}
	g.Close()
	if g.process != 0 || g.proc != nil {
		t.Fatal("Close retained the process handle")
	}
	if err := g.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel after Close = %v", err)
	}
}

func TestWindowsJobGuardFallbackRetainsExitedProcess(t *testing.T) {
	// No Job simulates creation/assignment failure: the guard must still pin
	// the process through Wait, and must not taskkill an already-exited PID.
	g := &windowsJobGuard{}
	defer g.Close()
	done := startWindowsGuardHelper(t, g, "exit")
	if g.process == 0 {
		t.Fatal("AfterStart did not retain a fallback process handle")
	}
	if err := awaitWindowsGuardHelper(t, done); err != nil {
		t.Fatal(err)
	}
	if status, err := windows.WaitForSingleObject(g.process, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("original process handle after Wait: status=%d, err=%v", status, err)
	}
	if err := g.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel after original exit = %v; want ErrProcessDone", err)
	}
	g.Close()
	if g.process != 0 || g.proc != nil {
		t.Fatal("Close retained the fallback process handle")
	}
}

func TestWindowsJobGuardCancelBeforeAfterStart(t *testing.T) {
	for _, withJob := range []bool{false, true} {
		name := "fallback"
		if withJob {
			name = "job"
		}
		t.Run(name, func(t *testing.T) {
			g := &windowsJobGuard{}
			if withJob {
				job, err := createKillOnCloseJob()
				if err != nil {
					t.Skipf("host cannot create Job: %v", err)
				}
				g.job = job
			}
			defer g.Close()
			// Force Start's cancellation watcher to win the ordering, without
			// sleeping or depending on scheduler timing.
			if err := g.Cancel(); err != nil {
				t.Fatal(err)
			}
			if !g.cancelled || g.closed {
				t.Fatal("early cancellation must remain pending for AfterStart")
			}
			done := startWindowsGuardHelper(t, g, "hold")
			if err := awaitWindowsGuardHelper(t, done); err == nil {
				t.Fatal("pending cancellation did not kill the process")
			}
		})
	}
}

func TestWindowsJobGuardCloseBeforeAfterStart(t *testing.T) {
	g := &windowsJobGuard{}
	g.Close()
	done := startWindowsGuardHelper(t, g, "hold")
	if err := awaitWindowsGuardHelper(t, done); err == nil {
		t.Fatal("process escaped Close racing with AfterStart")
	}
	if !g.closed || g.process != 0 || g.proc != nil {
		t.Fatal("AfterStart reopened a closed guard")
	}
}

func TestWindowsCommandCancellationAlwaysRegistersGuard(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), os.Args[0])
	guard := configureCommandCancellation(cmd)
	if guard == nil || cmd.Cancel == nil {
		t.Fatal("command did not register a process guard")
	}
	guard.Close()
}
