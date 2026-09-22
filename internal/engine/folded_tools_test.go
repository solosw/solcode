package engine

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/tool"
)

func names(prefix string, count int) []tool.Tool {
	out := make([]tool.Tool, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, &stubTool{
			name: prefix + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)),
			desc: "capability",
		})
	}
	return out
}

func mcpTool(server, name string) tool.Tool {
	return &stubTool{
		name: tool.MCPToolName(server, name),
		desc: "[MCP: " + server + "] does something",
	}
}

// The summary must name the tools that are loaded but unsent, because a model
// told only "more tools exist" has no reason to suspect anything relevant is
// missing and therefore never searches.
func TestFoldedToolsSummaryNamesUnsentTools(t *testing.T) {
	all := []tool.Tool{
		&stubTool{name: "View", desc: "view"},
		&stubTool{name: "ImageGenerate", desc: "generate"},
		&stubTool{name: "ImageEdit", desc: "edit"},
	}
	sent := []tool.Tool{&stubTool{name: "View", desc: "view"}}

	got := foldedToolsSummary(all, sent)
	if got == "" {
		t.Fatal("expected a summary")
	}
	for _, want := range []string{"ImageGenerate", "ImageEdit", "ToolSearch"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	// A tool already on the wire must not be listed as folded.
	if strings.Contains(got, "View") {
		t.Fatalf("summary should not list a tool that was sent:\n%s", got)
	}
}

// Hidden tools are never model-visible, so naming them would invite a call the
// engine will refuse.
func TestFoldedToolsSummarySkipsHiddenTools(t *testing.T) {
	all := []tool.Tool{
		&stubTool{name: tool.WaitToolName, desc: "wait"},
		&stubTool{name: tool.SubagentToolName, desc: "subagent"},
		&stubTool{name: "ImageGenerate", desc: "generate"},
	}
	got := foldedToolsSummary(all, nil)
	if strings.Contains(got, tool.WaitToolName) || strings.Contains(got, tool.SubagentToolName) {
		t.Fatalf("summary leaked a hidden tool:\n%s", got)
	}
	if !strings.Contains(got, "ImageGenerate") {
		t.Fatalf("summary missing the visible tool:\n%s", got)
	}
}

// MCP tools group by server so a large catalog stays compact.
func TestFoldedToolsSummaryGroupsMCPByServer(t *testing.T) {
	all := []tool.Tool{
		mcpTool("browser", "click"),
		mcpTool("browser", "type"),
		mcpTool("browser", "navigate"),
		mcpTool("files", "read"),
		&stubTool{name: "ImageGenerate", desc: "generate"},
	}
	got := foldedToolsSummary(all, nil)

	if !strings.Contains(got, "MCP browser") {
		t.Fatalf("expected a browser group:\n%s", got)
	}
	if !strings.Contains(got, "MCP files") {
		t.Fatalf("expected a files group:\n%s", got)
	}
	if !strings.Contains(got, "built-in") {
		t.Fatalf("expected a built-in group:\n%s", got)
	}
	// Grouped output must stay far shorter than listing every tool on its own.
	if len(got) > 600 {
		t.Fatalf("summary too long (%d chars):\n%s", len(got), got)
	}
}

// A group with more names than the cap collapses to a count instead of growing
// the prompt without bound.
func TestFoldedToolsSummaryTruncatesLongGroups(t *testing.T) {
	got := foldedToolsSummary(names("mcp__big__tool_", 40), nil)
	if !strings.Contains(got, "more)") {
		t.Fatalf("expected truncation marker:\n%s", got)
	}
	if strings.Count(got, "- ") > foldedSummaryMaxGroups {
		t.Fatalf("too many groups:\n%s", got)
	}
}

// Nothing folded means nothing added: an unchanged registry must not grow the
// prompt.
func TestFoldedToolsSummaryEmptyWhenNothingFolded(t *testing.T) {
	all := []tool.Tool{&stubTool{name: "View", desc: "view"}}
	sent := []tool.Tool{&stubTool{name: "View", desc: "view"}}
	if got := foldedToolsSummary(all, sent); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := foldedToolsSummary(nil, nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	// Only hidden tools folded is also empty, since they are never routable.
	hidden := []tool.Tool{&stubTool{name: tool.WaitToolName, desc: "wait"}}
	if got := foldedToolsSummary(hidden, nil); got != "" {
		t.Fatalf("got %q, want empty for hidden-only folds", got)
	}
}

// The summary must reach the model's message stream, and must tell it to search
// rather than assume the capability is missing.
func TestFoldedToolsReachMessages(t *testing.T) {
	summary := foldedToolsSummary([]tool.Tool{
		&stubTool{name: "ImageGenerate", desc: "generate"},
	}, nil)
	if summary == "" {
		t.Fatal("expected a summary")
	}
	builder := ContextBuilder{FoldedTools: summary}
	req := builder.Build(BuildRequest{
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))},
	})

	found := false
	for _, message := range req.Messages {
		text := messageText(message)
		if strings.Contains(text, "Additional capabilities available via ToolSearch") &&
			strings.Contains(text, "ImageGenerate") {
			found = true
			if !strings.Contains(text, "Do not assume a capability is missing") {
				t.Fatalf("summary must warn against assuming absence:\n%s", text)
			}
		}
	}
	if !found {
		t.Fatal("folded summary did not reach the message stream")
	}
}

func TestNoFoldedToolsLeavesMessagesUnchanged(t *testing.T) {
	builder := ContextBuilder{}
	req := builder.Build(BuildRequest{
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))},
	})
	for _, message := range req.Messages {
		if strings.Contains(messageText(message), "Additional capabilities") {
			t.Fatal("no folded tools should mean no summary block")
		}
	}
}

// The summary is deterministic, because a summary that reorders between turns
// would defeat prompt caching.
func TestFoldedToolsSummaryIsDeterministic(t *testing.T) {
	all := []tool.Tool{
		mcpTool("browser", "click"),
		mcpTool("browser", "type"),
		&stubTool{name: "ImageEdit", desc: "edit"},
		&stubTool{name: "ImageGenerate", desc: "generate"},
	}
	first := foldedToolsSummary(all, nil)
	for i := 0; i < 5; i++ {
		if got := foldedToolsSummary(all, nil); got != first {
			t.Fatalf("summary is not deterministic:\n%s\nvs\n%s", first, got)
		}
	}
}
