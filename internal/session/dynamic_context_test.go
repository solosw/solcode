package session

import (
	"reflect"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	contextmessage "github.com/solosw/solcode/internal/message"
)

func testDynamicMessage(kind, body string) sdk.MessageParam {
	return sdk.NewUserMessage(sdk.NewTextBlock(contextmessage.DynamicContextText(kind, body)))
}

func TestDynamicContextSurvivesCleanupRepairAndStorage(t *testing.T) {
	overlay := sdk.NewUserMessage(
		sdk.NewTextBlock(contextmessage.DynamicContextText("session-summary", "Session summary:\nbootstrap")),
		sdk.NewTextBlock(contextmessage.DynamicContextText("memory", "Retrieved memory:\npreference")),
	)
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("task")), overlay,
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("read", map[string]any{"path": "a.go"}, "View")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("read", "content", false)),
		testDynamicMessage("working-set", "file metadata"),
	}
	if got := StripEphemeralContextMessages(messages); !reflect.DeepEqual(got, messages) {
		t.Fatal("durable bootstrap blocks must not match legacy ephemeral cleanup")
	}
	if got, removed := RepairMessages(messages); removed != 0 || !reflect.DeepEqual(got, messages) {
		t.Fatal("overlay boundaries must preserve complete tool exchanges")
	}
	store := NewFileStore(t.TempDir())
	manager := NewManager(store, "main")
	s := NewSession("main", t.TempDir(), "test")
	s.ReplaceMessages(messages)
	if err := manager.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != len(messages) || !contextmessage.IsDynamicContext(loaded.Messages[1]) || !contextmessage.IsDynamicContext(loaded.Messages[4]) {
		t.Fatal("saved session lost recognizable durable overlay blocks")
	}
}

func TestDynamicContextDoesNotCountAsUserTurn(t *testing.T) {
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("first task")),
		testDynamicMessage("mode", "mode"),
		sdk.NewAssistantMessage(sdk.NewTextBlock("first result")),
		testDynamicMessage("efficiency", "advisory"),
		sdk.NewUserMessage(sdk.NewTextBlock("second task")),
		testDynamicMessage("working-set", "snapshot"),
		sdk.NewAssistantMessage(sdk.NewTextBlock("second result")),
	}
	if got := compactCutIndex(messages, 1); got != 4 {
		t.Fatalf("last real user turn starts at 4, got %d", got)
	}
	if got := compactCutIndex(messages, 2); got != 0 {
		t.Fatalf("two real turns should retain all history, got %d", got)
	}
	if got := previousTurnBoundary(messages, 4); got != 0 {
		t.Fatalf("previous real user turn starts at 0, got %d", got)
	}
}

func TestDynamicContextCompressionPreservesTags(t *testing.T) {
	block := sdk.NewTextBlock(contextmessage.DynamicContextText("working-set", strings.Repeat("file metadata and recorded versions\n", 1000)))
	compressed, kept, err := compressContentBlockWithHeadroom(block, 128, "user", nil)
	if err != nil || !kept || compressed.OfText == nil {
		t.Fatalf("compression failed: %v", err)
	}
	if kind, _, ok := contextmessage.ParseDynamicContextText(compressed.OfText.Text); !ok || kind != "working-set" {
		t.Fatal("compression must not turn internal metadata into a displayed user prompt")
	}
}

func TestBackgroundNotificationsAndCompactedContextDoNotStartUserTurns(t *testing.T) {
	for _, msg := range []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("<background-task-notification>\nstatus: success\nfinished output\n</background-task-notification>")),
		CompactedContextMessages("summary", "knowledge")[0],
		CompactedContextMessages("summary", "knowledge")[1],
	} {
		if messageStartsUserTurn(msg) {
			t.Fatal("synthetic context must not count as a new user task")
		}
	}
}
