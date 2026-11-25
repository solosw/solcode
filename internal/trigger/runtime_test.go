package trigger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(4 * time.Second)
	for !condition() {
		select {
		case <-deadline:
			t.Fatal("listener condition timed out")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestListenerIdleWakeHistoryIsolation(t *testing.T) {
	dir, store := t.TempDir(), t.TempDir()
	notified := make(chan struct{}, 1)
	runtime, err := New("current", dir, store, func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := runtime.Register(t.Context(), `while(sol.readText("event.txt")!=="ready"){await sol.sleep(25);} sol.trigger("Handle file event");`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
		t.Fatal("trigger fired before condition")
	case <-time.After(60 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(dir, "event.txt"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	case <-time.After(3 * time.Second):
		t.Fatal("idle session not notified")
	}
	event, ok := runtime.Next()
	if !ok || event.SessionID != "current" || event.WorkDir != dir || event.ListenerID != listener.ID || event.Prompt != "Handle file event" {
		t.Fatalf("wrong trigger: %+v", event)
	}
	if err := runtime.Mark(event, "running", nil); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Mark(event, "completed", nil); err != nil {
		t.Fatal(err)
	}
	other, err := New("other", dir, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	records, _ := other.History(0, 20)
	if len(records) != 0 {
		t.Fatal("history leaked across sessions")
	}
	await(t, func() bool {
		records, _ := runtime.History(0, 20)
		for _, e := range records {
			if e.ID == listener.ID {
				return e.Status == "completed"
			}
		}
		return false
	})
	runtime.Close()
	reopened, err := New("current", dir, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	records, _ = reopened.History(0, 20)
	found := false
	for _, e := range records {
		if e.ID == event.ID && e.Status == "completed" {
			found = true
		}
	}
	if !found {
		t.Fatal("completed event history did not persist")
	}
}

func TestManualStopCompletesStartedTrigger(t *testing.T) {
	for _, closeHost := range []bool{false, true} {
		runtime, err := New("current", t.TempDir(), t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(runtime.Close)
		runtime.mu.Lock()
		running := runtime.event("trigger", "running", "started", "")
		queued := runtime.event("trigger", "queued", "pending", "")
		runtime.records = append(runtime.records, running, queued)
		runtime.queue = append(runtime.queue, queued.ID)
		runtime.mu.Unlock()
		if closeHost {
			runtime.Close()
		} else {
			ctx, cancel := context.WithCancel(t.Context())
			if err := runtime.CancelRun(cancel); err != nil {
				t.Fatal(err)
			}
			if ctx.Err() != context.Canceled {
				t.Fatal("run was not interrupted")
			}
			if err := runtime.Mark(running, "cancelled", context.Canceled); err != nil {
				t.Fatal(err)
			}
		}
		records, _ := runtime.History(0, 20)
		for _, record := range records {
			if record.ID == running.ID {
				wantStatus, wantReason := "completed", "manual_stop"
				if closeHost {
					wantStatus, wantReason = "cancelled", ""
				}
				if record.Status != wantStatus || record.StopReason != wantReason || record.Error != "" {
					t.Fatalf("close=%v: %+v", closeHost, record)
				}
			}
			if record.ID == queued.ID && record.Status != "cancelled" {
				t.Fatalf("pending record: %+v", record)
			}
		}
		if _, ok := runtime.Next(); ok {
			t.Fatal("stopped session still queued work")
		}
	}
}

func TestCancelledRegistrationCannotInstallListener(t *testing.T) {
	runtime, err := New("current", t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	ctx, cancel := context.WithCancel(t.Context())
	runtime.mu.Lock()
	result := make(chan error, 1)
	go func() { _, err := runtime.Register(ctx, "sol.trigger('late');"); result <- err }()
	cancel()
	runtime.mu.Unlock()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled registration: %v", err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.listeners) != 0 || len(runtime.queue) != 0 || len(runtime.records) != 0 {
		t.Fatal("cancelled registration installed a listener")
	}
}

func TestListenerCancellationAndScriptFailures(t *testing.T) {
	runtime, err := New("current", t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.Register(t.Context(), "this is not JavaScript {"); err == nil {
		t.Fatal("invalid script accepted")
	}
	for _, code := range []string{
		`if(typeof process!=="undefined"||typeof require!=="undefined"||typeof fetch!=="undefined")throw Error("ambient access"); throw Error("expected failure");`,
		`sol.readText("../outside");`,
		`while(true){}`,
	} {
		listener, err := runtime.Register(t.Context(), code)
		if err != nil {
			t.Fatal(err)
		}
		await(t, func() bool {
			records, _ := runtime.History(0, 20)
			for _, e := range records {
				if e.ID == listener.ID {
					return e.Status == "failed"
				}
			}
			return false
		})
	}
	if _, err := runtime.Register(t.Context(), `while(true){await sol.sleep(50000);} `); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Register(t.Context(), `sol.trigger("pending");`); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		records, _ := runtime.History(0, 20)
		for _, e := range records {
			if e.Prompt == "pending" {
				return true
			}
		}
		return false
	})
	if err := runtime.Cancel(); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.Next(); ok {
		t.Fatal("cancelled queued trigger still runs")
	}
	started := time.Now()
	runtime.Close()
	if time.Since(started) > time.Second {
		t.Fatal("close did not stop sleeping listener")
	}
	records, _ := runtime.History(0, 20)
	for _, e := range records {
		if strings.Contains(e.Status, "listening") || e.Status == "queued" {
			t.Fatalf("listener not cancelled: %+v", e)
		}
	}
}
