package engine

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func TestContextBlockOmitsRecentSessionStateHeader(t *testing.T) {
	block := (ContextBuilder{}).contextBlock("Recent session state:\n- Continue custom provider setup", nil, "")
	if strings.Contains(block, "Recent session state:") {
		t.Fatalf("context block must omit the session summary heading: %q", block)
	}
	if !strings.Contains(block, "Continue custom provider setup") {
		t.Fatalf("context block = %q, want summary content", block)
	}
}

func TestContextBlockOmitsEmptySessionSummary(t *testing.T) {
	block := (ContextBuilder{}).contextBlock("Recent session state:", nil, "")
	if block != "" {
		t.Fatalf("context block = %q, want no empty session-summary block", block)
	}
}

func TestContextBlockOmitsNestedEmptySessionSummaryHeaders(t *testing.T) {
	block := (ContextBuilder{}).contextBlock("Session summary:\nRecent session state:", nil, "")
	if block != "" {
		t.Fatalf("context block = %q, want no nested empty session-summary block", block)
	}
}

func TestContextBlockOmitsViewPaginationSummaryNoise(t *testing.T) {
	summary := strings.Join([]string{
		"Recent session state:",
		"(File has 1038 more lines. Use 'offset' parameter to read beyond line 260)",
		"(File has 571 more lines. Use 'offset' parameter to read beyond line 425)",
	}, "\n")
	if block := (ContextBuilder{}).contextBlock(summary, nil, ""); block != "" {
		t.Fatalf("context block = %q, want pagination-only summary omitted", block)
	}
}

func TestContextBlockOmitsPlaceholderSessionSummary(t *testing.T) {
	summary := strings.Join([]string{
		"文件变更图上下文",
		"旧 session 对话压缩结果",
		"用户最新 prompt",
		"go test ./internal/app ./internal/engine ./internal/session ./cmd/solcode",
		"go build -o solcode.exe ./cmd/solcode",
	}, "\n")
	if block := (ContextBuilder{}).contextBlock(summary, nil, ""); block != "" {
		t.Fatalf("context block = %q, want placeholder summary omitted", block)
	}
}

func TestWithContextMessagesKeepsProjectSummaryAndLatestPromptOrder(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("latest user prompt")),
	}
	got := (ContextBuilder{}).withContextMessages(
		messages,
		"Recent session state:\n- previous session outcome",
		nil,
		"## Recent tracked changes\n- changed README",
	)
	if len(got) != 2 {
		t.Fatalf("message count = %d, want context plus latest prompt", len(got))
	}
	contextText := got[0].Content[0].OfText.Text
	if !strings.Contains(contextText, "Project knowledge context:") || !strings.Contains(contextText, "Session summary:") {
		t.Fatalf("composed context = %q, want both project knowledge and session summary", contextText)
	}
	if strings.Index(contextText, "Project knowledge context:") > strings.Index(contextText, "Session summary:") {
		t.Fatalf("context order = %q, want project knowledge before session summary", contextText)
	}
	promptText := got[1].Content[0].OfText.Text
	if promptText != "latest user prompt" {
		t.Fatalf("latest prompt = %q, want unchanged latest prompt", promptText)
	}
}
