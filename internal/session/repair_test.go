package session

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func TestRepairMessagesRemovesIncompleteToolUse(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("start")),
		sdk.NewAssistantMessage(
			sdk.NewTextBlock("I will try this."),
			sdk.NewToolUseBlock("toolu_incomplete", map[string]any{"command": "false"}, "Bash"),
		),
	}

	repaired, removed := RepairMessages(messages)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if len(repaired) != 2 {
		t.Fatalf("message count = %d, want 2", len(repaired))
	}
	if len(repaired[1].Content) != 1 || repaired[1].Content[0].OfText == nil {
		t.Fatalf("expected failed tool use removed while assistant text remains: %#v", repaired[1].Content)
	}
}

func TestRepairMessagesPreservesCompleteToolExchange(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("toolu_complete", map[string]any{"path": "main.go"}, "View")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("toolu_complete", "file content", false)),
	}

	repaired, removed := RepairMessages(messages)
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if len(repaired) != len(messages) {
		t.Fatalf("message count = %d, want %d", len(repaired), len(messages))
	}
	if repaired[0].Content[0].OfToolUse == nil || repaired[1].Content[0].OfToolResult == nil {
		t.Fatalf("expected complete tool exchange to remain intact")
	}
}

func TestRepairMessagesRemovesOrphanedToolResult(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewToolResultBlock("toolu_missing", "orphan", true)),
		sdk.NewUserMessage(sdk.NewTextBlock("continue")),
	}

	repaired, removed := RepairMessages(messages)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if len(repaired) != 1 || repaired[0].Content[0].OfText == nil {
		t.Fatalf("expected orphaned result message removed: %#v", repaired)
	}
}
