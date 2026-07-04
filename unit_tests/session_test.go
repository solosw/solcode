package unit_tests

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/codeplus-agent/internal/session"
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

type fakeSummaryWriter struct{}

func (fakeSummaryWriter) Summarize(ctx context.Context, previous string, newContent string) (string, error) {
	return strings.TrimSpace(previous + "\n" + newContent), nil
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
	result, err := session.Compact(ctx, "previous", messages, fakeSummaryWriter{}, session.CompactOptions{
		MaxRecentTurns:         2,
		SummaryThresholdTokens: 1,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !result.Changed {
		t.Fatal("expected compaction to change session")
	}
	if !strings.Contains(result.Summary, "previous") || !strings.Contains(result.Summary, "user turn") {
		t.Fatalf("summary did not include expected content: %q", result.Summary)
	}
	if len(result.Messages) >= len(messages) {
		t.Fatalf("expected fewer retained messages, before=%d after=%d", len(messages), len(result.Messages))
	}
	if got := session.ApproxTokensFromMessages(messages); got <= 0 {
		t.Fatalf("expected approximate token count, got %d", got)
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
