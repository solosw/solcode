package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/tool"
)

type timeoutTestTool struct{ name string }

func (t timeoutTestTool) Name() string              { return t.name }
func (timeoutTestTool) Description() string         { return "timeout test tool" }
func (timeoutTestTool) InputSchema() map[string]any { return nil }
func (timeoutTestTool) Invoke(context.Context, *tool.UseContext, json.RawMessage) (*tool.ContentBlock, error) {
	return tool.Result("ok"), nil
}
func (timeoutTestTool) IsDestructive(json.RawMessage) bool     { return false }
func (timeoutTestTool) IsReadOnly(json.RawMessage) bool        { return true }
func (timeoutTestTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (timeoutTestTool) Aliases() []string                      { return nil }
func (timeoutTestTool) ValidateInput(context.Context, json.RawMessage) error {
	return nil
}

func TestTimeoutForTaskToolIsThirtyMinutes(t *testing.T) {
	if got := timeoutForTool(timeoutTestTool{name: tool.TaskToolName}); got != 30*time.Minute {
		t.Fatalf("Task timeout = %s, want 30m", got)
	}
	if got := timeoutForTool(timeoutTestTool{name: tool.SubagentToolName}); got != 30*time.Minute {
		t.Fatalf("Subagent timeout = %s, want 30m", got)
	}
}

func TestTimeoutForBashAndWaitAllowTwentyFourHours(t *testing.T) {
	want := 24*time.Hour + 30*time.Second
	if got := timeoutForTool(timeoutTestTool{name: tool.BashToolName}); got != want {
		t.Fatalf("Bash timeout = %s, want %s", got, want)
	}
	if got := timeoutForTool(timeoutTestTool{name: tool.WaitToolName}); got != want {
		t.Fatalf("Wait timeout = %s, want %s", got, want)
	}
}

func TestTimeoutForRegularToolIsTwoMinutes(t *testing.T) {
	if got := timeoutForTool(timeoutTestTool{name: "Other"}); got != 2*time.Minute {
		t.Fatalf("regular tool timeout = %s, want 2m", got)
	}
}
