//go:build !windows

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashCancelAfterShellExit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bash := NewBashTool().(*bashTool)
	dir := t.TempDir()
	var stdout, stderr safeBuffer
	done := make(chan error, 1)
	go func() {
		_, err := bash.runCommandTo(ctx, "(sleep 0.6; echo survived > survived; sleep 5) & echo ready", dir, &stdout, &stderr)
		done <- err
	}()
	deadline := time.After(2 * time.Second)
	for stdout.String() == "" {
		select {
		case <-deadline:
			t.Fatal("command did not start")
		case <-time.After(time.Millisecond):
		}
	}
	// Let the shell exit while its child keeps the output pipes open.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Bash stayed blocked on output pipes after cancellation")
	}
	// Returning must not merely abandon the output pipes: the child itself
	// must stop before it can perform this delayed side effect.
	time.Sleep(650 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "survived")); !os.IsNotExist(err) {
		t.Fatal("child process survived Bash cancellation")
	}
}

func TestBatchPTCRealBashCompletionAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name, command, want string
		timeout             int
	}{
		{"child output", "(echo begin; sleep 0.6; echo end) &", "true begin\nend", 3000},
		{"deadline after shell exit", "sleep 5 & echo begin", "Command timed out", 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uctx, _ := batchTestContext(t)
			params, _ := json.Marshal(BashParams{Command: tc.command, Timeout: tc.timeout})
			result := runPTCTest(t, t.Context(), DefaultBatchLimits, uctx, "const r=await tools.Bash("+string(params)+"); console.log(r.ok,r.content);")
			if result.IsError || !strings.Contains(result.Text, tc.want) {
				t.Fatalf("unexpected Bash result: %+v", result)
			}
		})
	}
}

func TestBatchPTCRealBashCancellation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		timeout    int
		background bool
	}{
		{name: "foreground", timeout: DefaultTimeout},
		{name: "long timeout", timeout: MaxTimeout},
		{name: "background hint", timeout: DefaultTimeout, background: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			dir := t.TempDir()
			jobs := NewBackgroundJobs(4)
			bash := NewBashToolWithJobs(DefaultShell(), jobs)
			reg := NewRegistry()
			reg.Register(bash, NewWriteTool())
			finished := make(chan struct{})
			uctx := &UseContext{WorkDir: dir, BatchLookup: reg.Find}
			uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*ContentBlock, error) {
				if name == BashToolName {
					defer close(finished)
				}
				return reg.Find(name).Invoke(ctx, uctx, input)
			}
			params, _ := json.Marshal(BashParams{Command: "sleep 5 & echo ready > ready", Timeout: tc.timeout, RunInBackground: tc.background})
			code := "await tools.Bash(" + string(params) + "); await tools.Write({path:'must-not-run',content:'x'});"
			input, _ := json.Marshal(map[string]string{"code": code})
			done := make(chan *ContentBlock, 1)
			go func() {
				result, _ := NewBatchToolWithPTC(DefaultBatchLimits, true).Invoke(ctx, uctx, input)
				done <- result
			}()
			deadline := time.After(2 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
					break
				}
				select {
				case <-deadline:
					t.Fatal("Bash did not start")
				case <-time.After(time.Millisecond):
				}
			}
			time.Sleep(100 * time.Millisecond)
			cancel()
			select {
			case result := <-done:
				if result == nil || !result.IsError || !strings.Contains(result.Text, "context canceled") {
					t.Fatalf("cancellation lost: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("PTC remained blocked after Bash cancellation")
			}
			select {
			case <-finished:
			default:
				t.Fatal("PTC returned before Bash exited")
			}
			if ids := jobs.RunningIDs(); len(ids) != 0 {
				t.Fatalf("Bash jobs survived cancellation: %v", ids)
			}
			if _, err := os.Stat(filepath.Join(dir, "must-not-run")); !os.IsNotExist(err) {
				t.Fatal("PTC ran a dependent mutation after cancellation")
			}
		})
	}
}
