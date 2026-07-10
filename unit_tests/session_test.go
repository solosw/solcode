package unit_tests

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tokenest"
)

func TestSessionFileStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := session.NewFileStore(t.TempDir())
	s := session.NewSession("main", t.TempDir(), "test-model")
	s.Summary = "previous work summary"
	s.Append(sdk.NewUserMessage(sdk.NewTextBlock("hello")))

	if err := store.Save(ctx, s); err != nil {
		t.Fatalf("save session: %v", err)
	}
	loaded, err := store.Load(ctx, "main")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.Metadata.ID != "main" {
		t.Fatalf("expected session id main, got %q", loaded.Metadata.ID)
	}
	if loaded.Summary != s.Summary {
		t.Fatalf("expected summary %q, got %q", s.Summary, loaded.Summary)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(loaded.Messages))
	}
}

func TestSessionManagerSaveStripsEphemeralContextMessages(t *testing.T) {
	ctx := context.Background()
	manager := session.NewManager(session.NewFileStore(t.TempDir()), "main")
	s := session.NewSession("main", t.TempDir(), "test-model")
	s.Append(
		sdk.NewUserMessage(sdk.NewTextBlock("Session summary:\nold summary\n\nRetrieved memory:\n- stale memory")),
		sdk.NewUserMessage(sdk.NewTextBlock("real user request")),
		sdk.NewUserMessage(sdk.NewTextBlock("Retrieved memory:\n- stale memory only")),
	)

	if err := manager.Save(ctx, s); err != nil {
		t.Fatalf("save session: %v", err)
	}
	loaded, err := manager.LoadOrCreate(ctx, "main", "", "")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("expected only real message to persist, got %d", len(loaded.Messages))
	}
	transcript := session.Transcript(loaded.Messages)
	if strings.Contains(transcript, "Session summary:") || strings.Contains(transcript, "Retrieved memory:") {
		t.Fatalf("expected ephemeral context to be stripped, got transcript %q", transcript)
	}
	if !strings.Contains(transcript, "real user request") {
		t.Fatalf("expected real user request to remain, got transcript %q", transcript)
	}
}

func TestSessionCompactStripsEphemeralContextMessages(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("Session summary:\nold summary\n\nRetrieved memory:\n- stale memory")),
		sdk.NewUserMessage(sdk.NewTextBlock("real user request")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("real assistant response")),
	}

	result, err := session.Compact(ctx, "", messages, nil, session.CompactOptions{
		SummaryThresholdTokens: 1000,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if result.Changed {
		t.Fatal("expected no compaction when below threshold")
	}
	transcript := session.Transcript(result.Messages)
	if strings.Contains(transcript, "Session summary:") || strings.Contains(transcript, "Retrieved memory:") {
		t.Fatalf("expected compact result to strip ephemeral context, got transcript %q", transcript)
	}
	if !strings.Contains(transcript, "real user request") || !strings.Contains(transcript, "real assistant response") {
		t.Fatalf("expected real conversation to remain, got transcript %q", transcript)
	}
}

func TestSessionCompactKeepsRecentTurnsAndUpdatesSummary(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{}
	for i := 0; i < 5; i++ {
		messages = append(messages,
			sdk.NewUserMessage(sdk.NewTextBlock("user turn")),
			sdk.NewAssistantMessage(sdk.NewTextBlock("assistant turn")),
		)
	}
	result, err := session.Compact(ctx, "previous", messages, nil, session.CompactOptions{
		MaxRecentTurns:         2,
		SummaryThresholdTokens: 1,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !result.Changed {
		t.Fatal("expected compaction to change session")
	}
	if strings.TrimSpace(result.Summary) != "" {
		t.Fatalf("expected summary to be folded into compressed session, got %q", result.Summary)
	}
	if len(result.Messages) >= len(messages)+1 {
		t.Fatalf("expected compacted session to stay bounded, before=%d after=%d", len(messages), len(result.Messages))
	}
	if !strings.Contains(result.OriginalTranscript, "previous") || !strings.Contains(result.OriginalTranscript, "user turn") {
		t.Fatalf("expected original transcript to include prior summary and user turn, got %q", result.OriginalTranscript)
	}
	if strings.TrimSpace(result.CompactedTranscript) == "" {
		t.Fatal("expected compacted transcript to be populated")
	}
	if got := session.ApproxTokensFromMessages(messages); got <= 0 {
		t.Fatalf("expected approximate token count, got %d", got)
	}
	if got, want := session.ApproxTokensFromMessages(messages), tokenest.Messages(messages); got != want {
		t.Fatalf("session.ApproxTokensFromMessages() = %d, tokenest.Messages() = %d", got, want)
	}
}

func TestSessionCompactTargetTokensShrinksRetained(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{}
	for i := 0; i < 10; i++ {
		messages = append(messages,
			sdk.NewUserMessage(sdk.NewTextBlock("user turn with some text")),
			sdk.NewAssistantMessage(sdk.NewTextBlock("assistant turn with some text")),
		)
	}
	// With TargetTokens=1 we should keep the absolute minimum tail.
	result, err := session.Compact(ctx, "previous", messages, nil, session.CompactOptions{
		MaxRecentTurns:         20,
		SummaryThresholdTokens: 1,
		TargetTokens:           1,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !result.Changed {
		t.Fatal("expected compaction to change session")
	}
	if len(result.Messages) >= len(messages) {
		t.Fatalf("expected fewer retained messages with target tokens, before=%d after=%d", len(messages), len(result.Messages))
	}
}

func TestSessionCompactUsesEstimatedContextTokens(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("older user turn")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("older assistant turn")),
		sdk.NewUserMessage(sdk.NewTextBlock("recent user turn")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("recent assistant turn")),
	}
	if session.ApproxTokensFromMessages(messages) >= 1000 {
		t.Fatal("test setup expected message-only estimate to stay below threshold")
	}
	result, err := session.Compact(ctx, "previous", messages, nil, session.CompactOptions{
		MaxRecentTurns:         1,
		SummaryThresholdTokens: 1000,
		EstimatedTokens:        1000,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !result.Changed {
		t.Fatal("expected estimated context tokens to trigger compaction")
	}
}

func TestSessionCompactPreservesStructuredToolIDs(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("toolu_keep", map[string]any{"file_path": "main.go", "old_string": "a", "new_string": "b"}, "Edit")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("toolu_keep", strings.Repeat("large tool output ", 200), true)),
	}
	result, err := session.Compact(ctx, "previous context", messages, nil, session.CompactOptions{
		MaxRecentTurns:         20,
		SummaryThresholdTokens: 1,
		TargetTokens:           1000,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !result.Changed {
		t.Fatal("expected compaction to change session")
	}
	if len(result.Messages) != 3 {
		t.Fatalf("expected previous context plus both tool messages to remain, got %d", len(result.Messages))
	}
	toolUse := result.Messages[1].Content[0].OfToolUse
	if toolUse == nil {
		t.Fatalf("expected first block to remain tool_use, got %#v", result.Messages[0].Content[0])
	}
	if toolUse.ID != "toolu_keep" || toolUse.Name != "Edit" {
		t.Fatalf("tool_use identity changed: id=%q name=%q", toolUse.ID, toolUse.Name)
	}
	toolResult := result.Messages[2].Content[0].OfToolResult
	if toolResult == nil {
		t.Fatalf("expected third block to remain tool_result, got %#v", result.Messages[2].Content[0])
	}
	if toolResult.ToolUseID != "toolu_keep" {
		t.Fatalf("tool_result id changed: %q", toolResult.ToolUseID)
	}
	if !toolResult.IsError.Valid() || !toolResult.IsError.Value {
		t.Fatalf("expected tool_result is_error=true to be preserved, got valid=%v value=%v", toolResult.IsError.Valid(), toolResult.IsError.Value)
	}
	if len(toolResult.Content) == 0 || toolResult.Content[0].OfText == nil || strings.TrimSpace(toolResult.Content[0].OfText.Text) == "" {
		t.Fatalf("expected compressed tool_result text content to remain, got %#v", toolResult.Content)
	}
}

func TestSessionCompactDoesNotCutBetweenToolUseAndToolResult(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{
		sdk.NewAssistantMessage(sdk.NewTextBlock("older assistant")),
		sdk.NewUserMessage(sdk.NewTextBlock("older user")),
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("toolu_cut_guard", map[string]any{"command": "echo hi"}, "Bash")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("toolu_cut_guard", strings.Repeat("tool output ", 200), false)),
		sdk.NewAssistantMessage(sdk.NewTextBlock("recent assistant")),
		sdk.NewUserMessage(sdk.NewTextBlock("recent user")),
	}
	result, err := session.Compact(ctx, "", messages, nil, session.CompactOptions{
		MaxRecentTurns:         1,
		SummaryThresholdTokens: 1,
		TargetTokens:           1,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !result.Changed {
		t.Fatal("expected compaction to change session")
	}
	for i, msg := range result.Messages {
		for _, block := range msg.Content {
			if block.OfToolResult != nil && block.OfToolResult.ToolUseID == "toolu_cut_guard" {
				if i == 0 {
					t.Fatalf("tool_result retained without prior tool_use: %#v", result.Messages)
				}
				prevHasToolUse := false
				for _, prevBlock := range result.Messages[i-1].Content {
					if prevBlock.OfToolUse != nil && prevBlock.OfToolUse.ID == "toolu_cut_guard" {
						prevHasToolUse = true
						break
					}
				}
				if !prevHasToolUse {
					t.Fatalf("tool_result retained without matching prior tool_use: %#v", result.Messages)
				}
			}
		}
	}
}

func TestSessionCompactDropsBashButPreservesEditLikeTools(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("toolu_bash", map[string]any{"command": "go test ./..."}, "Bash")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("toolu_bash", strings.Repeat("bash output ", 100), false)),
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("toolu_edit", map[string]any{"file_path": "a.txt", "old_string": "a", "new_string": "b"}, "Edit")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("toolu_edit", "applied", false)),
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("toolu_fetch", map[string]any{"url": "https://example.com"}, "Fetch")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("toolu_fetch", strings.Repeat("fetch output ", 100), false)),
	}
	result, err := session.Compact(ctx, "", messages, nil, session.CompactOptions{
		MaxRecentTurns:         20,
		SummaryThresholdTokens: 1,
		TargetTokens:           1000,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	transcript := session.Transcript(result.Messages)
	if strings.Contains(transcript, "toolu_bash") || strings.Contains(transcript, "go test ./...") || strings.Contains(transcript, "bash output") {
		t.Fatalf("expected bash blocks removed entirely, got transcript %q", transcript)
	}
	if !strings.Contains(transcript, "file_path") || !strings.Contains(transcript, "[tool result]\napplied") {
		t.Fatalf("expected edit tool payload and result preserved, got transcript %q", transcript)
	}
	if !strings.Contains(transcript, "toolu_fetch") {
		t.Fatalf("expected summarized tool to preserve full tool id, got transcript %q", transcript)
	}
}

func TestSessionCompactDropsUnmatchedToolUseWithoutResult(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("toolu_orphan", map[string]any{"command": "go test ./..."}, "Fetch")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("later assistant text")),
	}
	result, err := session.Compact(ctx, "", messages, nil, session.CompactOptions{
		MaxRecentTurns:         20,
		SummaryThresholdTokens: 1,
		TargetTokens:           1000,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	transcript := session.Transcript(result.Messages)
	if strings.Contains(transcript, "toolu_orphan") || strings.Contains(transcript, "tool call preserved as summarized metadata") {
		t.Fatalf("expected unmatched tool_use to be removed, got transcript %q", transcript)
	}
}

func TestSessionCompactDropsUnmatchedToolResultWithoutToolUse(t *testing.T) {
	ctx := context.Background()
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewToolResultBlock("toolu_missing", strings.Repeat("tool output ", 50), false)),
		sdk.NewUserMessage(sdk.NewTextBlock("real user turn")),
	}
	result, err := session.Compact(ctx, "", messages, nil, session.CompactOptions{
		MaxRecentTurns:         20,
		SummaryThresholdTokens: 1,
		TargetTokens:           1000,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	transcript := session.Transcript(result.Messages)
	if strings.Contains(transcript, "toolu_missing") || strings.Contains(transcript, "tool result preserved as summarized metadata") {
		t.Fatalf("expected unmatched tool_result to be removed, got transcript %q", transcript)
	}
	if !strings.Contains(transcript, "real user turn") {
		t.Fatalf("expected non-tool content to remain, got transcript %q", transcript)
	}
}

func TestSessionMetadataCrossSessionMemoryPersists(t *testing.T) {
	ctx := context.Background()
	store := session.NewFileStore(t.TempDir())
	cross := true
	s := session.NewSession("main", t.TempDir(), "model")
	s.Metadata.CrossSessionMemory = &cross
	s.Append(sdk.NewUserMessage(sdk.NewTextBlock("hello")))
	if err := store.Save(ctx, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.Load(ctx, "main")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Metadata.CrossSessionMemory == nil || !*loaded.Metadata.CrossSessionMemory {
		t.Fatalf("expected cross_session_memory to persist as true, got %v", loaded.Metadata.CrossSessionMemory)
	}
}

func TestSessionManagerLoadOrCreate(t *testing.T) {
	ctx := context.Background()
	manager := session.NewManager(session.NewFileStore(t.TempDir()), "main")
	s, err := manager.LoadOrCreate(ctx, "", "wd", "model")
	if err != nil {
		t.Fatalf("load or create: %v", err)
	}
	if s.Metadata.ID != "main" {
		t.Fatalf("expected default session id main, got %q", s.Metadata.ID)
	}
	if err := manager.Save(ctx, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := manager.LoadOrCreate(ctx, "main", "wd2", "model2")
	if err != nil {
		t.Fatalf("load existing: %v", err)
	}
	if loaded.Metadata.WorkDir != "wd2" || loaded.Metadata.Model != "model2" {
		t.Fatalf("expected metadata update, got workdir=%q model=%q", loaded.Metadata.WorkDir, loaded.Metadata.Model)
	}
}
