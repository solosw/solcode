package session

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

type evidenceSummaryWriter struct {
	transcript string
	calls      int
}

func (w *evidenceSummaryWriter) Summarize(_ context.Context, previous, transcript string) (string, error) {
	w.transcript = transcript
	w.calls++
	return previous, nil
}

func TestForcedCompactGeneratesSummaryBelowGate(t *testing.T) {
	writer := &evidenceSummaryWriter{}
	result, err := Compact(t.Context(), "existing handoff", []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("new task"))}, writer, CompactOptions{Force: true, SummaryThresholdTokens: 100_000, SummaryIfAboveTokens: 50_000})
	if err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || !result.SummaryGenerated || result.Summary != "existing handoff" {
		t.Fatalf("summary generation = calls %d, result %+v", writer.calls, result)
	}
}

func TestCompactSummaryReadsEvidenceBeforeMasking(t *testing.T) {
	output := "FAIL TestKeepHandoff: expected original handoff; exit code 1\n" + strings.Repeat("test details ", 100)
	messages := []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("old task")),
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("test", map[string]any{"command": "go test ./internal/app"}, "Bash")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("test", output, true)),
		sdk.NewUserMessage(sdk.NewTextBlock("recent task")),
	}
	writer := &evidenceSummaryWriter{}
	result, err := Compact(t.Context(), "existing handoff", messages, writer, CompactOptions{Force: true, ObservationKeepTurns: 1, ObservationMinChars: 50, ObservationStore: NewFileObservationStore(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writer.transcript, "FAIL TestKeepHandoff") || !strings.Contains(writer.transcript, "exit code 1") || strings.Contains(writer.transcript, ObservationMaskMarker) {
		t.Fatalf("writer did not receive original evidence: %q", writer.transcript)
	}
	if !result.SummaryGenerated {
		t.Fatal("unchanged summary must still be marked generated")
	}
}

func TestCompactedSummaryUsesLatestNonEmptyDurableMessage(t *testing.T) {
	messages := []sdk.MessageParam{NewCompactedSummaryMessage("first"), sdk.NewUserMessage(sdk.NewTextBlock("ordinary user message")), NewCompactedSummaryMessage("latest"), NewCompactedSummaryMessage("")}
	if got := CompactedSummary(messages); got != "latest" {
		t.Fatalf("summary = %q", got)
	}
	if got := CompactedSummary(nil); got != "" {
		t.Fatalf("empty summary = %q", got)
	}
}
