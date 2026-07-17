package unit_tests

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tokenest"
	"github.com/solosw/solcode/internal/tool"
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

func TestTokenEstimatorImageTokens(t *testing.T) {
	if got := tokenest.ImageTokens(0, 0); got != tokenest.MinImageTokens {
		t.Fatalf("ImageTokens(0,0) = %d, want %d", got, tokenest.MinImageTokens)
	}
	// 750x750 → 750 tokens exactly after formula.
	if got, want := tokenest.ImageTokens(750, 750), 750; got != want {
		t.Fatalf("ImageTokens(750,750) = %d, want %d", got, want)
	}
	// Messages with an image block must count more than text-only.
	img := attachImageBlock(t, 100) // 100 bytes of fake base64 payload → some tokens
	withImg := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("look"), img),
	}
	textOnly := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("look")),
	}
	if tokenest.Messages(withImg) <= tokenest.Messages(textOnly) {
		t.Fatalf("messages with image (%d) should exceed text-only (%d)", tokenest.Messages(withImg), tokenest.Messages(textOnly))
	}
	if tokenest.MessageImageTokens(withImg) <= 0 {
		t.Fatal("MessageImageTokens should be > 0")
	}
}

// attachImageBlock builds a minimal base64 image content block for token tests.
func attachImageBlock(t *testing.T, rawBytes int) sdk.ContentBlockParamUnion {
	t.Helper()
	if rawBytes < 1 {
		rawBytes = 1
	}
	// base64 of zero bytes of length rawBytes
	data := make([]byte, rawBytes)
	encoded := strings.Repeat("A", (rawBytes*4+2)/3) // approximate base64 length
	_ = data
	return sdk.ContentBlockParamUnion{
		OfImage: &sdk.ImageBlockParam{
			Source: sdk.ImageBlockParamSourceUnion{
				OfBase64: &sdk.Base64ImageSourceParam{
					MediaType: "image/png",
					Data:      encoded,
				},
			},
		},
	}
}
