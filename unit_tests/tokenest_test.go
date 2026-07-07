package unit_tests

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	cpanthropic "github.com/solosw/codeplus-agent/internal/anthropic"
	"github.com/solosw/codeplus-agent/internal/session"
	"github.com/solosw/codeplus-agent/internal/tokenest"
	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestTokenEstimatorTextAndMessages(t *testing.T) {
	if got := tokenest.Text(""); got != 0 {
		t.Fatalf("Text(empty) = %d, want 0", got)
	}
	if got := tokenest.Text("abcd"); got != 2 {
		t.Fatalf("Text(\"abcd\") = %d, want 2", got)
	}

	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("hello")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("world")),
	}
	if got := tokenest.Messages(messages); got <= 0 {
		t.Fatalf("Messages() = %d, want > 0", got)
	}
	if got, want := tokenest.Messages(messages), session.ApproxTokensFromMessages(messages); got != want {
		t.Fatalf("Messages() = %d, session.ApproxTokensFromMessages() = %d", got, want)
	}
}

func TestTokenEstimatorTranscriptIncludesToolContent(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("tool_1", map[string]any{"path": "a.txt"}, "View")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("tool_1", "file contents", false)),
	}
	transcript := tokenest.Transcript(messages)
	for _, want := range []string{"assistant: [tool use: View]", `"path":"a.txt"`, "user: [tool result]", "file contents"} {
		if transcript == "" || !strings.Contains(transcript, want) {
			t.Fatalf("expected transcript %q to contain %q", transcript, want)
		}
	}
}

func TestTokenEstimatorTools(t *testing.T) {
	tools := []sdk.ToolUnionParam{cpanthropic.ToolToSDK(tool.NewViewTool())}
	if got := tokenest.Tools(tools); got <= 0 {
		t.Fatalf("Tools() = %d, want > 0", got)
	}
}

func TestTokenEstimatorRequest(t *testing.T) {
	messages := []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))}
	tools := []sdk.ToolUnionParam{cpanthropic.ToolToSDK(tool.NewViewTool())}
	got := tokenest.Request("system prompt", messages, tools)
	want := tokenest.Text("system prompt") + tokenest.Messages(messages) + tokenest.Tools(tools)
	if got != want {
		t.Fatalf("Request() = %d, want %d", got, want)
	}
}
