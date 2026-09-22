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

func TestHookBuiltin_CompressesEdit(t *testing.T) {
	rt := hook.NewRuntime(hook.DefaultConfig())

	// Edit is no longer in SkipTools: large repetitive payloads should compress.
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("updated file section line=")
		b.WriteString(strings.Repeat("e", 48))
		b.WriteString(" ok\n")
	}
	raw := b.String()
	res, err := rt.Run(context.Background(), hook.Event{
		Name:       hook.EventPostToolUse,
		ToolName:   "Edit",
		ToolResult: tool.Result(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != hook.DecisionModify {
		t.Fatalf("expected Edit to compress, got %q msg=%q", res.Decision, res.Message)
	}
	block := hook.ApplyModifiedResult(nil, res.ModifiedResult)
	if block == nil || len(block.Text) >= len(raw) {
		t.Fatalf("expected shorter Edit result: before=%d after=%v", len(raw), block)
	}
	t.Logf("Edit compressed: before=%d after=%d msg=%q", len(raw), len(block.Text), res.Message)
}

func TestHookBuiltin_CompressesMCPTool(t *testing.T) {
	rt := hook.NewRuntime(hook.DefaultConfig())

	// MCP tools are registered as mcp__<server>__<tool> and go through the same
	// ToolExecutor PostToolUse path with matcher "*".
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("mcp tool dump row=")
		b.WriteString(strings.Repeat("m", 48))
		b.WriteString(" status=ok\n")
	}
	raw := b.String()
	name := "mcp__filesystem__read_file"
	res, err := rt.Run(context.Background(), hook.Event{
		Name:       hook.EventPostToolUse,
		ToolName:   name,
		ToolResult: tool.Result(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != hook.DecisionModify {
		t.Fatalf("expected MCP tool %s to compress, got %q msg=%q", name, res.Decision, res.Message)
	}
	block := hook.ApplyModifiedResult(nil, res.ModifiedResult)
	if block == nil || len(block.Text) >= len(raw) {
		t.Fatalf("expected shorter MCP result: before=%d after=%v", len(raw), block)
	}
	if !strings.Contains(res.Message, "mcp__") {
		t.Fatalf("hook message should mention MCP tool name, got %q", res.Message)
	}
	t.Logf("MCP compressed: tool=%s before=%d after=%d msg=%q", name, len(raw), len(block.Text), res.Message)
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

// TestToolExecutor_PostToolUseCompressesPerTool exercises the default PostToolUse
// compressor once per built-in tool name and reports before/after sizes.
func TestToolExecutor_PostToolUseCompressesPerTool(t *testing.T) {
	var dump strings.Builder
	for i := 0; i < 400; i++ {
		dump.WriteString("INFO worker finished task id=")
		dump.WriteString(strings.Repeat("a", 32))
		dump.WriteString(" status=ok payload=")
		dump.WriteString(strings.Repeat("b", 64))
		dump.WriteByte('\n')
	}
	raw := dump.String()
	before := len(raw)

	// Write tools (Edit/Write/…) are compressed by default now.
	skipTools := map[string]bool{}
	// Built-in tool surface (text results). ViewImage is covered separately as image.
	tools := []string{
		"AskUser", "Bash", "Diff", "Edit", "Fetch", "Glob", "Grep", "LS",
		"ModeSwitch", "MultiEdit", "MultiWrite", "Patch", "ReadMemory", "Skill",
		"Task", "TodoWrite", "ToolSearch", "View", "WebSearch", "Write", "WriteMemory",
		"mcp__demo__echo",
	}

	for _, name := range tools {
		name := name
		t.Run(name, func(t *testing.T) {
			reg := tool.NewRegistry()
			reg.Register(&staticTextTool{name: name, text: raw})
			exec := engine.NewToolExecutor(reg, hook.NewRuntime(hook.DefaultConfig()))
			out := exec.Execute(context.Background(), engine.ToolCall{
				Name:  name,
				Input: json.RawMessage(`{}`),
			}, engine.ToolEnv{UseContext: &tool.UseContext{WorkDir: t.TempDir()}})
			if out.IsError {
				t.Fatalf("tool error: %s", out.Content.Text)
			}
			after := len(out.Content.Text)
			modified := after != before
			t.Logf("tool=%-12s before=%d after=%d delta=%d modified=%v preview=%q",
				name, before, after, before-after, modified, truncate(out.Content.Text, 96))

			if skipTools[name] {
				if out.Content.Text != raw {
					t.Fatalf("skip tool %s must keep full result: before=%d after=%d", name, before, after)
				}
				return
			}
			// View uses structure-preserving compression (no headroom mid-fold).
			// This fixture has no long lines / blank runs, so the text is unchanged.
			if name == "View" {
				if out.Content.Text != raw {
					t.Fatalf("View fixture without long/blank runs must stay intact: before=%d after=%d", before, after)
				}
				if strings.Contains(out.Content.Text, "more lines") {
					t.Fatalf("View must not structurally elide lines: %q", truncate(out.Content.Text, 200))
				}
				return
			}
			if !modified {
				t.Fatalf("tool %s expected compression, before=%d after=%d", name, before, after)
			}
			if after >= before {
				t.Fatalf("tool %s expected shorter text, before=%d after=%d", name, before, after)
			}
			if strings.Contains(out.Content.Text, "tool output compressed for context") {
				t.Fatalf("tool %s result must not include compression banner", name)
			}
		})
	}

	t.Run("ViewImage_image_skipped", func(t *testing.T) {
		reg := tool.NewRegistry()
		img := &tool.ContentBlock{Type: "image", MimeType: "image/png", Data: strings.Repeat("A", 4096)}
		reg.Register(&staticBlockTool{name: "ViewImage", block: img})
		exec := engine.NewToolExecutor(reg, hook.NewRuntime(hook.DefaultConfig()))
		out := exec.Execute(context.Background(), engine.ToolCall{
			Name:  "ViewImage",
			Input: json.RawMessage(`{}`),
		}, engine.ToolEnv{UseContext: &tool.UseContext{WorkDir: t.TempDir()}})
		if out.IsError {
			t.Fatalf("tool error: %s", out.Content.Text)
		}
		if out.Content.Type != "image" || out.Content.Data != img.Data {
			t.Fatalf("image tool result must not be rewritten: %+v", out.Content)
		}
		t.Logf("tool=ViewImage type=image data_len=%d unchanged=true", len(out.Content.Data))
	})

	t.Run("Bash_error_skipped", func(t *testing.T) {
		reg := tool.NewRegistry()
		reg.Register(&staticBlockTool{name: "Bash", block: tool.ErrorResult(raw)})
		exec := engine.NewToolExecutor(reg, hook.NewRuntime(hook.DefaultConfig()))
		out := exec.Execute(context.Background(), engine.ToolCall{
			Name:  "Bash",
			Input: json.RawMessage(`{}`),
		}, engine.ToolEnv{UseContext: &tool.UseContext{WorkDir: t.TempDir()}})
		if !out.IsError && !out.Content.IsError {
			t.Fatal("expected error result")
		}
		if out.Content.Text != raw {
			t.Fatalf("error result must not be compressed: before=%d after=%d", before, len(out.Content.Text))
		}
		t.Logf("tool=Bash error=true before=%d after=%d unchanged=true", before, len(out.Content.Text))
	})
}

func TestHookBuiltin_ViewPreservesStructure(t *testing.T) {
	// Realistic View payload: numbered lines, blank runs, and one overlong line.
	var b strings.Builder
	b.WriteString("<file>\n")
	b.WriteString("     1|package demo\n")
	b.WriteString("     2|\n")
	b.WriteString("     3|\n") // consecutive blanks -> collapse to one
	b.WriteString("     4|\n")
	b.WriteString("     5|func main() {\n")
	b.WriteString("     6|\tfmt.Println(\"")
	b.WriteString(strings.Repeat("x", 2500))
	b.WriteString("\")\n")
	b.WriteString("     7|}\n")
	b.WriteString("     8|// trailing note stays visible\n")
	b.WriteString("</file>\n")
	raw := b.String()

	rt := hook.NewRuntime(hook.DefaultConfig())
	result, err := rt.Run(context.Background(), hook.Event{
		Name:       hook.EventPostToolUse,
		ToolName:   "View",
		ToolResult: tool.Result(raw),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Decision != hook.DecisionModify {
		t.Fatalf("expected modify for long-line/blank-run View payload, got %q msg=%q", result.Decision, result.Message)
	}
	block := hook.ApplyModifiedResult(nil, result.ModifiedResult)
	if block == nil {
		t.Fatal("expected modified block")
	}
	out := block.Text
	if strings.Contains(out, "more lines") {
		t.Fatalf("View compression must not fold middle lines: %q", truncate(out, 300))
	}
	// All non-blank logical rows should remain addressable by content markers.
	for _, want := range []string{
		"package demo",
		"func main()",
		"trailing note stays visible",
		"... [truncated]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in compressed View output:\n%s", want, out)
		}
	}
	// Consecutive View-numbered blanks ("N|") collapse to a single blank row.
	// Fixture blanks are lines 2/3/4; only the first blank marker should remain.
	if strings.Contains(out, "     3|") || strings.Contains(out, "     4|") {
		t.Fatalf("expected consecutive numbered blank lines collapsed, still have mid blanks:\n%s", out)
	}
	if !strings.Contains(out, "     2|") {
		t.Fatalf("expected first blank numbered line retained:\n%s", out)
	}
	if len(out) >= len(raw) {
		t.Fatalf("expected some savings from long-line truncate / blank collapse: before=%d after=%d", len(raw), len(out))
	}
	t.Logf("View structure preserve: before=%d after=%d message=%q", len(raw), len(out), result.Message)
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

type staticBlockTool struct {
	name  string
	block *tool.ContentBlock
}

func (s *staticBlockTool) Name() string                                         { return s.name }
func (s *staticBlockTool) Description() string                                  { return "static-block" }
func (s *staticBlockTool) InputSchema() map[string]any                          { return map[string]any{} }
func (s *staticBlockTool) IsDestructive(json.RawMessage) bool                   { return false }
func (s *staticBlockTool) IsReadOnly(json.RawMessage) bool                      { return true }
func (s *staticBlockTool) IsConcurrencySafe(json.RawMessage) bool               { return true }
func (s *staticBlockTool) Aliases() []string                                    { return nil }
func (s *staticBlockTool) ValidateInput(context.Context, json.RawMessage) error { return nil }
func (s *staticBlockTool) Invoke(context.Context, *tool.UseContext, json.RawMessage) (*tool.ContentBlock, error) {
	cp := *s.block
	return &cp, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
