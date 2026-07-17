package unit_tests

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/tokenest"
)

func TestToolResultBlock_IncludesImage(t *testing.T) {
	block := cpanthropic.ToolResultBlock(cpanthropic.ToolResult{
		ToolUseID:     "toolu_img",
		Text:          "Image: shot.png\n~100 vision tokens",
		ImageMimeType: "image/jpeg",
		// short fake base64 payload is fine for structure checks
		ImageData: "AAAA",
	})
	if block.OfToolResult == nil {
		t.Fatal("expected tool_result block")
	}
	tr := block.OfToolResult
	if tr.ToolUseID != "toolu_img" {
		t.Fatalf("tool_use_id = %q", tr.ToolUseID)
	}
	if len(tr.Content) < 2 {
		t.Fatalf("expected text+image content, got %d parts", len(tr.Content))
	}
	if tr.Content[0].OfText == nil || tr.Content[0].OfText.Text == "" {
		t.Fatal("expected text content first")
	}
	if tr.Content[1].OfImage == nil || tr.Content[1].OfImage.Source.OfBase64 == nil {
		t.Fatal("expected base64 image content")
	}
	if string(tr.Content[1].OfImage.Source.OfBase64.MediaType) != "image/jpeg" {
		t.Fatalf("media type = %q", tr.Content[1].OfImage.Source.OfBase64.MediaType)
	}
	if tr.Content[1].OfImage.Source.OfBase64.Data != "AAAA" {
		t.Fatalf("image data = %q", tr.Content[1].OfImage.Source.OfBase64.Data)
	}
}

func TestToolResultBlocks_TextOnlyUnchanged(t *testing.T) {
	blocks := cpanthropic.ToolResultBlocks([]cpanthropic.ToolResult{
		{ToolUseID: "toolu_1", Text: "ok", IsError: false},
	})
	if len(blocks) != 1 || blocks[0].OfToolResult == nil {
		t.Fatal("expected one tool_result")
	}
	content := blocks[0].OfToolResult.Content
	if len(content) != 1 || content[0].OfText == nil || content[0].OfText.Text != "ok" {
		t.Fatalf("unexpected content: %+v", content)
	}
}

func TestMessageImageTokens_CountsToolResultImages(t *testing.T) {
	// Build a user message with tool_result carrying an image.
	// ImageTokensFromBytes uses raw≈base64*3/4 then /1125; need enough bytes to exceed MinImageTokens.
	// 200_000 base64 chars ≈ 150_000 raw → ~133 tokens.
	data := make([]byte, 200_000)
	for i := range data {
		data[i] = 'A'
	}
	msg := sdk.NewUserMessage(cpanthropic.ToolResultBlock(cpanthropic.ToolResult{
		ToolUseID:     "toolu_img",
		Text:          "caption",
		ImageMimeType: "image/png",
		ImageData:     string(data),
	}))
	got := tokenest.MessageImageTokens([]sdk.MessageParam{msg})
	if got <= tokenest.MinImageTokens {
		t.Fatalf("MessageImageTokens = %d, want > min %d", got, tokenest.MinImageTokens)
	}
	// Messages should include image estimate (not just text caption).
	total := tokenest.Messages([]sdk.MessageParam{msg})
	if total < got {
		t.Fatalf("Messages total %d < image tokens %d", total, got)
	}
}
