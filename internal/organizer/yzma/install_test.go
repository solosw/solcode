package yzma

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestInstallerRejectsConcurrentInstall covers the single-flight guard: the
// libraries share one directory, so a second concurrent install would race on
// the same files.
func TestInstallerRejectsConcurrentInstall(t *testing.T) {
	installer := NewInstaller()
	// Start a run that cannot finish: a cancelled parent context plus a
	// nonexistent processor keeps it from touching the network in a way that
	// matters, but the guard is checked before any work starts.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := installer.Start(ctx, t.TempDir(), "cpu")
	if err != nil {
		// The guard is only meaningful if the first start was accepted, so a
		// rejected first start means the test cannot exercise the guard.
		t.Skipf("first Start was rejected (%v); nothing to guard", err)
	}

	second := installer.Start(context.Background(), t.TempDir(), "cpu")
	if second == nil {
		t.Fatal("expected the second Start to be rejected")
	}
	if !strings.Contains(second.Error(), "already running") {
		t.Fatalf("second Start error = %v, want an 'already running' rejection", second)
	}

	installer.Cancel()
}

func TestInstallerRequiresLibDir(t *testing.T) {
	installer := NewInstaller()
	if err := installer.Start(context.Background(), "  ", "cpu"); err == nil {
		t.Fatal("expected an error for an empty library directory")
	}
}

func TestInstallerRejectsUnknownProcessor(t *testing.T) {
	installer := NewInstaller()
	err := installer.Start(context.Background(), t.TempDir(), "not-a-processor")
	if err == nil {
		t.Fatal("expected an error for an unknown processor")
	}
}

func TestInstallerIdleByDefault(t *testing.T) {
	installer := NewInstaller()
	status := installer.Status()
	if status.State != InstallIdle {
		t.Fatalf("state = %q, want idle", status.State)
	}
	if status.Percent != -1 {
		t.Fatalf("percent = %v, want -1 for an unknown total", status.Percent)
	}
	if installer.Running() {
		t.Fatal("a fresh installer must not report Running")
	}
}

func TestInstallerNilIsSafe(t *testing.T) {
	var installer *Installer
	if installer.Running() {
		t.Fatal("a nil installer must not report Running")
	}
	if status := installer.Status(); status.State != InstallIdle {
		t.Fatalf("nil installer state = %q, want idle", status.State)
	}
	installer.Cancel() // must not panic
	if err := installer.Start(context.Background(), t.TempDir(), "cpu"); err == nil {
		t.Fatal("expected a nil installer to reject Start")
	}
}

func TestInstallerOnChangeIsInvoked(t *testing.T) {
	installer := NewInstaller()
	states := make(chan InstallState, 16)
	installer.SetOnChange(func(status InstallStatus) {
		select {
		case states <- status.State:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := installer.Start(ctx, t.TempDir(), "cpu"); err != nil {
		t.Skipf("Start was rejected (%v); cannot observe callbacks", err)
	}

	select {
	case state := <-states:
		if state != InstallRunning {
			t.Fatalf("first observed state = %q, want running", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no status callback was observed")
	}
}

// TestAssetName covers the display helper used for the progress message.
func TestAssetName(t *testing.T) {
	cases := map[string]string{
		"https://example.com/releases/llama-b9433-bin-win-cpu-x64.zip": "llama-b9433-bin-win-cpu-x64.zip",
		"https://example.com/a.tar.gz?token=abc":                       "a.tar.gz",
		"https://example.com/dir/":                                     "dir",
		"":                                                             "",
	}
	for input, want := range cases {
		if got := assetName(input); got != want {
			t.Fatalf("assetName(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestCountingReaderReportsBytes covers the progress plumbing: the tracker
// wraps the download stream and must forward every byte read.
func TestCountingReaderReportsBytes(t *testing.T) {
	source := io.NopCloser(strings.NewReader("abcdefghij"))
	var last int64
	counter := &countingReader{
		reader:  source,
		total:   10,
		onBytes: func(n int64) { last = n },
	}

	buf := make([]byte, 4)
	total := 0
	for {
		n, err := counter.Read(buf)
		total += n
		if err != nil {
			break
		}
	}
	if total != 10 {
		t.Fatalf("read %d bytes, want 10", total)
	}
	if last != 10 {
		t.Fatalf("last reported byte count = %d, want 10", last)
	}
	if err := counter.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestSetProgressComputesPercent covers the percentage math, including the
// unknown-total case that must stay indeterminate rather than guess.
func TestSetProgressComputesPercent(t *testing.T) {
	installer := &Installer{status: InstallStatus{State: InstallRunning}}

	installer.setProgress("https://example.com/llama.zip", 200, 50)
	if got := installer.Status().Percent; got != 25 {
		t.Fatalf("percent = %v, want 25", got)
	}

	installer.setProgress("https://example.com/llama.zip", 0, 50)
	if got := installer.Status().Percent; got != -1 {
		t.Fatalf("percent = %v, want -1 when the total is unknown", got)
	}
}

// TestSetProgressIsIgnoredWhenNotRunning guards against a late progress callback
// rewriting a settled state.
func TestSetProgressIsIgnoredWhenNotRunning(t *testing.T) {
	installer := &Installer{status: InstallStatus{State: InstallDone, Percent: 100}}
	installer.setProgress("https://example.com/llama.zip", 200, 10)
	status := installer.Status()
	if status.Percent != 100 || status.Bytes != 0 {
		t.Fatalf("settled status was mutated: %#v", status)
	}
}

func TestInstallStateConstants(t *testing.T) {
	// The frontend switches on these exact strings; keep them stable.
	for _, pair := range []struct {
		state InstallState
		want  string
	}{
		{InstallIdle, "idle"},
		{InstallRunning, "running"},
		{InstallDone, "done"},
		{InstallFailed, "failed"},
		{InstallCancelled, "cancelled"},
	} {
		if string(pair.state) != pair.want {
			t.Fatalf("state constant %q != %q", pair.state, pair.want)
		}
	}
}
