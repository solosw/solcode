//go:build !windows

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/tool"
)

func TestPTCEngineCancelsRealBashAfterShellExit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dir := t.TempDir()
	registry := tool.NewRegistry()
	registry.Register(tool.NewBatchToolWithPTC(tool.DefaultBatchLimits, true), tool.NewBashTool(), tool.NewWriteTool())
	var calls atomic.Int32
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		if calls.Add(1) == 1 {
			return backgroundMessage(t, backgroundCall("ptc", tool.BatchToolName, map[string]any{"code": `await tools.Bash({command:"sleep 5 & echo ready > ready"}); await tools.Write({path:"must-not-run",content:"x"});`})), nil
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, Hooks: hook.NewRuntime(hook.DefaultConfig()), MaxTurns: 3})
	done := make(chan RunResult, 1)
	go func() {
		done <- eng.RunWithHistory(ctx, RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Prompt: "run PTC", WorkDir: dir}})
	}()
	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("PTC Bash did not start")
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case result := <-done:
		if result.AgentResult.Error != context.Canceled.Error() || calls.Load() != 1 {
			t.Fatalf("engine continued after cancellation: %+v calls=%d", result.AgentResult, calls.Load())
		}
		texts := backgroundResultTexts(result.Messages, "ptc")
		if len(texts) != 1 || !strings.Contains(texts[0], "context canceled") {
			t.Fatalf("canceled tool result not retained: %v", texts)
		}
	case <-time.After(time.Second):
		t.Fatal("engine stayed blocked in PTC Bash after cancellation")
	}
	if _, err := os.Stat(filepath.Join(dir, "must-not-run")); !os.IsNotExist(err) {
		t.Fatal("dependent mutation ran after cancellation")
	}
}
