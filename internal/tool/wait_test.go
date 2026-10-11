package tool

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sleepCommand(seconds int) string {
	if runtime.GOOS == "windows" {
		// ping -n N waits roughly N-1 seconds
		return "ping -n " + strconv.Itoa(seconds+1) + " 127.0.0.1 > nul"
	}
	return "sleep " + strconv.Itoa(seconds)
}

func TestBashAutoWaitOnLongTimeout(t *testing.T) {
	prev := autoWaitThresholdMs
	autoWaitThresholdMs = 500 // treat >500ms as long for the test
	t.Cleanup(func() { autoWaitThresholdMs = prev })

	jobs := NewBackgroundJobs(8)
	bash := NewBashToolWithJobs(DefaultShell(), jobs)

	input, _ := json.Marshal(map[string]any{
		"command": sleepCommand(1),
		"timeout": 5_000, // above threshold → auto-wait path
	})
	res, err := bash.Invoke(context.Background(), &UseContext{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("auto-wait failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "status: completed") {
		t.Fatalf("expected completed job output, got:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "task_id:") {
		t.Fatalf("expected task_id in snapshot:\n%s", res.Text)
	}
}

func TestBashShortTimeoutStaysForeground(t *testing.T) {
	prev := autoWaitThresholdMs
	autoWaitThresholdMs = 60_000
	t.Cleanup(func() { autoWaitThresholdMs = prev })

	jobs := NewBackgroundJobs(8)
	bash := NewBashToolWithJobs(DefaultShell(), jobs)

	cmd := "echo hello"
	if runtime.GOOS == "windows" {
		cmd = "echo hello"
	}
	input, _ := json.Marshal(map[string]any{
		"command": cmd,
		"timeout": 5_000, // below threshold
	})
	res, err := bash.Invoke(context.Background(), &UseContext{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("foreground bash failed: %s", res.Text)
	}
	if strings.Contains(res.Text, "task_id:") {
		t.Fatalf("short timeout should not use job snapshot format: %q", res.Text)
	}
	if !strings.Contains(strings.ToLower(res.Text), "hello") {
		t.Fatalf("expected command output, got %q", res.Text)
	}
	if len(jobs.List()) != 0 {
		t.Fatalf("short runs must not register background jobs")
	}
}

func TestBashAutoWaitTimeoutWhileRunning(t *testing.T) {
	prev := autoWaitThresholdMs
	autoWaitThresholdMs = 200
	t.Cleanup(func() { autoWaitThresholdMs = prev })

	jobs := NewBackgroundJobs(8)
	bash := NewBashToolWithJobs(DefaultShell(), jobs)

	// Job lifetime and wait are both the Bash timeout (200ms threshold path
	// with a short absolute timeout so the wait ends while the OS sleep is mid-flight).
	input, _ := json.Marshal(map[string]any{
		"command": sleepCommand(5),
		"timeout": 400, // > threshold, short absolute wait
	})
	start := time.Now()
	res, err := bash.Invoke(context.Background(), &UseContext{}, input)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("auto-wait should return near bash timeout, took %s", elapsed)
	}
	// Job may finish as timed_out (job ctx) or still report running if wait raced.
	if !strings.Contains(res.Text, "status:") {
		t.Fatalf("expected job status output:\n%s", res.Text)
	}
	// Cancel anything still running.
	for _, id := range jobs.RunningIDs() {
		jobs.Cancel(id)
		_, _ = jobs.Wait(context.Background(), id, 5*time.Second)
	}
}

func TestWaitToolStillWorksInternally(t *testing.T) {
	jobs := NewBackgroundJobs(8)
	bash := NewBashToolWithJobs(DefaultShell(), jobs).(*bashTool)
	wait := NewWaitToolWithJobs(jobs)

	id := jobs.Start(sleepCommand(1), "", 30*time.Second, func(ctx context.Context, stdout, stderr *safeBuffer) (int, error) {
		return bash.runCommandTo(ctx, sleepCommand(1), "", stdout, stderr)
	})

	waitInput, _ := json.Marshal(map[string]any{"task_id": id, "timeout_ms": 15_000})
	waitRes, err := wait.Invoke(context.Background(), &UseContext{}, waitInput)
	if err != nil {
		t.Fatal(err)
	}
	if waitRes.IsError {
		t.Fatalf("wait failed: %s", waitRes.Text)
	}
	if !strings.Contains(waitRes.Text, "status: completed") {
		t.Fatalf("expected completed, got:\n%s", waitRes.Text)
	}
}

func TestWaitUnknownTask(t *testing.T) {
	wait := NewWaitToolWithJobs(NewBackgroundJobs(4))
	input, _ := json.Marshal(map[string]any{"task_id": "bash-missing"})
	res, err := wait.Invoke(context.Background(), &UseContext{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Text, "unknown") {
		t.Fatalf("expected unknown error, got %q", res.Text)
	}
}

func TestWaitNoJobs(t *testing.T) {
	wait := NewWaitToolWithJobs(NewBackgroundJobs(4))
	res, err := wait.Invoke(context.Background(), &UseContext{}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Text, "No background") {
		t.Fatalf("got %q", res.Text)
	}
}

func TestBackgroundJobCancel(t *testing.T) {
	jobs := NewBackgroundJobs(4)
	bash := NewBashToolWithJobs(DefaultShell(), jobs).(*bashTool)
	command := sleepCommand(30)
	id := jobs.Start(command, "", 60*time.Second, func(ctx context.Context, stdout, stderr *safeBuffer) (int, error) {
		return bash.runCommandTo(ctx, command, "", stdout, stderr)
	})
	if !jobs.Cancel(id) {
		t.Fatal("cancel returned false")
	}
	snap, err := jobs.Wait(context.Background(), id, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != JobCanceled && snap.Status != JobFailed && snap.Status != JobCompleted {
		t.Fatalf("status after cancel = %s", snap.Status)
	}
	if snap.Status == JobCompleted && snap.Elapsed > 3*time.Second {
		t.Fatalf("job appears to have run to completion despite cancel: %+v", snap)
	}
}

func TestBashWaitsForContinuousStdoutUntilExit(t *testing.T) {
	prev := autoWaitThresholdMs
	autoWaitThresholdMs = 500
	t.Cleanup(func() { autoWaitThresholdMs = prev })

	dir := t.TempDir()
	scriptPath := dir + string(os.PathSeparator) + "train.py"
	script := "import time\nprint('start', flush=True)\ntime.sleep(2)\nprint('end', flush=True)\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	jobs := NewBackgroundJobs(4)
	bash := NewBashToolWithJobs(DefaultShell(), jobs)
	// Avoid strconv.Quote: cmd.exe keeps the quotes as part of the path.
	command := "python " + scriptPath
	input, _ := json.Marshal(map[string]any{
		"command": command,
		"timeout": 15_000, // auto-wait path
	})
	start := time.Now()
	res, err := bash.Invoke(context.Background(), &UseContext{}, input)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "end") || !strings.Contains(res.Text, "status: completed") {
		t.Fatalf("expected completed full output:\n%s", res.Text)
	}
	if elapsed < 1800*time.Millisecond {
		t.Fatalf("returned too early (%s) — treated first stdout as completion?", elapsed)
	}
}

func TestMaxTimeoutIsTwentyFourHours(t *testing.T) {
	if MaxTimeout != 86_400_000 {
		t.Fatalf("MaxTimeout = %d, want 86400000 (24h)", MaxTimeout)
	}
	if MaxWaitTimeoutMs != MaxTimeout {
		t.Fatalf("MaxWaitTimeoutMs = %d, want same as MaxTimeout %d", MaxWaitTimeoutMs, MaxTimeout)
	}
	if AutoWaitThresholdMs != 180_000 {
		t.Fatalf("AutoWaitThresholdMs = %d, want 180000 (3m)", AutoWaitThresholdMs)
	}
}

func TestBashLongJobCancellationDoesNotOrphanCommand(t *testing.T) {
	jobs := NewBackgroundJobs(4)
	bash := NewBashToolWithJobs(DefaultShell(), jobs)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input, _ := json.Marshal(BashParams{Command: sleepCommand(10), Timeout: MaxTimeout})
	returned := make(chan *ContentBlock, 1)
	go func() {
		result, err := bash.Invoke(ctx, &UseContext{}, input)
		if err != nil {
			result = ErrorResult(err.Error())
		}
		returned <- result
	}()
	deadline := time.After(2 * time.Second)
	var ids []string
	for len(ids) == 0 {
		select {
		case <-deadline:
			t.Fatal("long command did not start")
		case <-time.After(time.Millisecond):
			ids = jobs.RunningIDs()
		}
	}
	cancel()
	select {
	case result := <-returned:
		if !result.IsError {
			t.Fatalf("canceled wait reported success: %s", result.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Bash did not return after cancellation")
	}
	snapshot, err := jobs.Wait(t.Context(), ids[0], 2*time.Second)
	if err != nil || snapshot.Status != JobCanceled {
		t.Fatalf("command survived its parent cancellation: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestBackgroundJobsStartWithContext(t *testing.T) {
	jobs := NewBackgroundJobs(4)
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	id := jobs.StartWithContext(ctx, "test", "", time.Minute, func(ctx context.Context, _, _ *safeBuffer) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	})
	<-started
	cancel()
	snapshot, err := jobs.Wait(t.Context(), id, time.Second)
	if err != nil || snapshot.Status != JobCanceled {
		t.Fatalf("snapshot = %#v; error=%v", snapshot, err)
	}
}
