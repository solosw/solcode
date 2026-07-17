package unit_tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/tool"
)

func TestHookBuiltin_CompressToolResult(t *testing.T) {
	// Repeated log-style dump (headroom compresses this aggressively).
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("INFO worker finished task id=")
		b.WriteString(strings.Repeat("a", 32))
		b.WriteString(" status=ok payload=")
		b.WriteString(strings.Repeat("b", 64))
		b.WriteByte('\n')
	}
	raw := b.String()

	rt := hook.NewRuntime(hook.DefaultConfig())
	result, err := rt.Run(context.Background(), hook.Event{
		Name:       hook.EventPostToolUse,
		ToolName:   "Bash",
		ToolResult: tool.Result(raw),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Decision != hook.DecisionModify {
		t.Fatalf("expected modify, got %q (message=%q)", result.Decision, result.Message)
	}
	block := hook.ApplyModifiedResult(nil, result.ModifiedResult)
	if block == nil || block.Text == "" {
		t.Fatal("expected modified text block")
	}
	if strings.Contains(block.Text, "tool output compressed for context") {
		t.Fatalf("tool_result must not include compression banner: %q", truncate(block.Text, 200))
	}
	if !strings.Contains(result.Message, "compressed") {
		t.Fatalf("expected hook message to record compression, got %q", result.Message)
	}
	if len(block.Text) >= len(raw) {
		t.Fatalf("expected shorter text: before=%d after=%d", len(raw), len(block.Text))
	}
}

func TestHookBuiltin_SkipsEditAndSmall(t *testing.T) {
	rt := hook.NewRuntime(hook.DefaultConfig())

	// Small payload: allow (no modify).
	res, err := rt.Run(context.Background(), hook.Event{
		Name:       hook.EventPostToolUse,
		ToolName:   "Bash",
		ToolResult: tool.Result("ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ModifiedResult != nil {
		t.Fatal("small result should not be modified")
	}

	// Edit tool: skip even if huge.
	huge := strings.Repeat("edit payload line\n", 2000)
	res, err = rt.Run(context.Background(), hook.Event{
		Name:       hook.EventPostToolUse,
		ToolName:   "Edit",
		ToolResult: tool.Result(huge),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ModifiedResult != nil {
		t.Fatal("Edit results must not be compressed")
	}
}

func TestToolExecutor_PostToolUseBuiltinCompresses(t *testing.T) {
	var dump strings.Builder
	for i := 0; i < 500; i++ {
		dump.WriteString("INFO worker finished task id=")
		dump.WriteString(strings.Repeat("a", 32))
		dump.WriteString(" status=ok payload=")
		dump.WriteString(strings.Repeat("b", 64))
		dump.WriteByte('\n')
	}

	reg := tool.NewRegistry()
	reg.Register(&staticTextTool{name: "Bash", text: dump.String()})

	exec := engine.NewToolExecutor(reg, hook.NewRuntime(hook.DefaultConfig()))
	out := exec.Execute(context.Background(), engine.ToolCall{
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"echo"}`),
	}, engine.ToolEnv{UseContext: &tool.UseContext{WorkDir: t.TempDir()}})

	if out.IsError {
		t.Fatalf("error: %s", out.Content.Text)
	}
	if strings.Contains(out.Content.Text, "tool output compressed for context") {
		t.Fatalf("tool_result must not include compression banner: %q", truncate(out.Content.Text, 120))
	}
	if len(out.Content.Text) >= len(dump.String()) {
		t.Fatalf("expected compression, before=%d after=%d", len(dump.String()), len(out.Content.Text))
	}
}

type staticTextTool struct {
	name string
	text string
}

func (s *staticTextTool) Name() string                                         { return s.name }
func (s *staticTextTool) Description() string                                  { return "static" }
func (s *staticTextTool) InputSchema() map[string]any                          { return map[string]any{} }
func (s *staticTextTool) IsDestructive(json.RawMessage) bool                   { return false }
func (s *staticTextTool) IsReadOnly(json.RawMessage) bool                      { return true }
func (s *staticTextTool) IsConcurrencySafe(json.RawMessage) bool               { return true }
func (s *staticTextTool) Aliases() []string                                    { return nil }
func (s *staticTextTool) ValidateInput(context.Context, json.RawMessage) error { return nil }
func (s *staticTextTool) Invoke(context.Context, *tool.UseContext, json.RawMessage) (*tool.ContentBlock, error) {
	return tool.Result(s.text), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
