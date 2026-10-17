package engine

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	contextmessage "github.com/solosw/solcode/internal/message"
	"github.com/solosw/solcode/internal/tool"
)

func TestForkSourceExcludesCurrentCallAndModeOverlays(t *testing.T) {
	reg := tool.NewRegistry()
	eng := NewEngine(Config{Tools: reg})
	w := NewWorkingSet()
	w.AddFact("retained fact")
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("parent request")),
		sdk.NewUserMessage(sdk.NewTextBlock(contextmessage.DynamicContextText("mode", "old mode"))),
		sdk.NewUserMessage(sdk.NewTextBlock(contextmessage.DynamicContextText("session-summary", "parent summary"))),
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("completed", map[string]any{}, "View")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("completed", "original read", false)),
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("unfinished", map[string]any{}, "Task")),
	}
	inherited := eng.forkSource(tool.TaskToolName, messages, w)
	if len(inherited.Messages) != 4 {
		t.Fatalf("inherited %d messages", len(inherited.Messages))
	}
	for _, m := range inherited.Messages {
		for _, b := range m.Content {
			if b.OfToolUse != nil && b.OfToolUse.ID == "unfinished" {
				t.Fatal("unfinished call inherited")
			}
		}
	}
	if len(inherited.WorkingSet) == 0 {
		t.Fatal("working set missing")
	}
	if other := eng.forkSource("View", messages, w); len(other.Messages) > 0 || len(other.WorkingSet) > 0 {
		t.Fatal("ordinary tool unnecessarily snapshots parent context")
	}
}
