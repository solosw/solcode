package message

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func TestDynamicContextOnlyClassifiesPureKnownOverlays(t *testing.T) {
	block := sdk.NewTextBlock(DynamicContextText("mode", "policy"))
	if !IsDynamicContext(sdk.NewUserMessage(block)) {
		t.Fatal("generated overlay not recognized")
	}
	if !IsDynamicContext(sdk.NewUserMessage(block, sdk.NewTextBlock(DynamicContextText("agent-role", "Explorer")))) {
		t.Fatal("role overlay must not be treated as a real user turn")
	}
	for _, msg := range []sdk.MessageParam{
		sdk.NewAssistantMessage(block),
		sdk.NewUserMessage(block, sdk.NewTextBlock("real task")),
		sdk.NewUserMessage(block, sdk.NewToolResultBlock("tool", "result", false)),
		sdk.NewUserMessage(sdk.NewTextBlock(DynamicContextText("unknown", "not an overlay"))),
		sdk.NewUserMessage(sdk.NewTextBlock("<solcode-context kind=\"mode\">\ntruncated")),
	} {
		if IsDynamicContext(msg) {
			t.Fatal("ordinary/mixed/malformed messages must not be hidden")
		}
	}
}
