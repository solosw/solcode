package unit_tests

import (
	"context"
	"encoding/json"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/tool"
)

func TestBashTool_IsDestructive(t *testing.T) {
	b := tool.NewBashTool()
	if !b.IsDestructive(nil) {
		t.Fatal("Bash should be destructive")
	}
	if b.IsReadOnly(nil) {
		t.Fatal("Bash should NOT be read-only")
	}
}

func TestTruncateOutput(t *testing.T) {
	long := strings.Repeat("x", 1000)
	short := tool.TruncateOutput(long, 100)
	if len(short) > 150 {
		t.Fatalf("expected truncated (<150), got len=%d", len(short))
	}
	if tool.TruncateOutput("hello", 100) != "hello" {
		t.Fatal("short content should be unchanged")
	}
}

func TestBashToolCancellationStopsCommandImmediately(t *testing.T) {
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "ping -n 30 127.0.0.1 > nul"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tool.NewBashTool().Invoke(ctx, &tool.UseContext{}, json.RawMessage(`{"command":`+strconv.Quote(command)+`}`))
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Bash invocation did not return promptly after cancellation")
	}
}
