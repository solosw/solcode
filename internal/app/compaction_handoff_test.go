package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

func testSessionHandoff() string {
	return strings.Join([]string{
		"## Active Request and Intent", "- 修复会话压缩，保留用户原始约束。",
		"## Constraints and Decisions", "- Do not change the summary-only retention policy.",
		"## Progress and Current State", "- done: implemented summary generation; pending: verification.",
		"## Files and Symbols", "- `internal/app/app.go`: `compactSession` preserves the generated handoff.",
		"## Errors and Verification", "- `go test ./internal/app` failed; exit code 1.",
		"```text", "## Next Step", "return status = failed", "```",
		"## Open Tasks and Blockers", "- None",
		"## Current Work", "- None",
		"## Next Step", "- Re-run targeted tests; result not yet verified.",
	}, "\n")
}

func TestHandoffSummarySanitizationPreservesStructure(t *testing.T) {
	summary := testSessionHandoff()
	for _, input := range []string{summary, summary + "\n(File has 1038 more lines. Use 'offset' parameter to read beyond line 260)"} {
		got := sanitizeLoadedSessionSummary(input)
		if got != summary {
			t.Fatalf("handoff changed during cleanup:\n%s", got)
		}
		if again := sanitizeLoadedSessionSummary(got); again != got {
			t.Fatal("handoff cleanup is not idempotent")
		}
	}
}

func TestForcedCompactionKeepsGeneratedHandoff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target int
	}{{"small target", 1}, {"unchanged transcript", 100}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.MaxContextTokens = 100_000
			cfg.Memory.CompactionTargetPercent = tc.target
			writer := &recordingSummaryWriter{summary: testSessionHandoff()}
			application := &App{Config: cfg, summaryWriter: writer}
			current := session.NewSession("named", t.TempDir(), cfg.Model)
			outcome := "investigated"
			if tc.target == 1 {
				outcome = strings.Repeat("investigated compaction evidence and handoff state. ", 1000)
			}
			current.Append(sdk.NewUserMessage(sdk.NewTextBlock("fix compaction")), sdk.NewAssistantMessage(sdk.NewTextBlock(outcome)))
			changed, err := application.compactSession(context.Background(), current, true)
			if err != nil || !changed {
				t.Fatalf("compact = %v, %v", changed, err)
			}
			if !writer.called || current.Summary != writer.summary {
				t.Fatalf("generated handoff was skipped or replaced: called=%v summary=%q", writer.called, current.Summary)
			}
			if got := session.CompactedSummary(current.Messages); got != writer.summary {
				t.Fatalf("durable handoff = %q", got)
			}
			store := session.NewFileStore(t.TempDir())
			if err := store.Save(t.Context(), current); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(t.Context(), "named")
			if err != nil {
				t.Fatal(err)
			}
			application.SanitizeLoadedSession(loaded)
			if loaded.Summary != writer.summary || session.CompactedSummary(loaded.Messages) != writer.summary {
				t.Fatal("save/reload changed handoff")
			}
		})
	}
}

func TestCompactionUsesDurableHandoffOverStaleField(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100_000
	writer := &recordingSummaryWriter{summary: "replacement handoff"}
	application := &App{Config: cfg, summaryWriter: writer}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Summary = "stale field"
	current.Append(session.NewCompactedSummaryMessage(testSessionHandoff()), sdk.NewUserMessage(sdk.NewTextBlock("continue the fix")))
	if _, err := application.compactSession(t.Context(), current, true); err != nil {
		t.Fatal(err)
	}
	if writer.previous != testSessionHandoff() {
		t.Fatalf("previous = %q", writer.previous)
	}
	if strings.Contains(writer.transcript, "## Active Request") {
		t.Fatal("durable summary duplicated into new transcript")
	}
}

func TestConsecutiveMidRunCompactionsPreservePreviousHandoff(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100_000
	writer := &recordingSummaryWriter{summary: testSessionHandoff()}
	application := &App{Config: cfg, summaryWriter: writer}
	first, err := application.compactMessagesMidRun(t.Context(), []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("fix compaction"))})
	if err != nil {
		t.Fatal(err)
	}
	writer.called = false
	writer.summary = "second handoff with earlier decisions"
	first = append(first, sdk.NewUserMessage(sdk.NewTextBlock("continue with validation")))
	second, err := application.compactMessagesMidRun(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if !writer.called || writer.previous != testSessionHandoff() {
		t.Fatalf("previous handoff lost: called=%v previous=%q", writer.called, writer.previous)
	}
	if session.CompactedSummary(second) != writer.summary {
		t.Fatal("second handoff was replaced")
	}
}

func TestForcedCompactionFailurePreservesHistory(t *testing.T) {
	for _, failure := range []string{"summary service failed", "session summary truncated at output token limit"} {
		t.Run(failure, func(t *testing.T) {
			writer := &recordingSummaryWriter{err: fmt.Errorf("%s", failure)}
			application := &App{Config: config.Default(), summaryWriter: writer}
			current := session.NewSession("named", t.TempDir(), "model")
			persistCompactedContext(current, testSessionHandoff(), "")
			current.Append(sdk.NewUserMessage(sdk.NewTextBlock("unfinished task")))
			before := session.Transcript(current.Messages)
			changed, err := application.compactSession(t.Context(), current, true)
			if err == nil || !strings.Contains(err.Error(), failure) || changed {
				t.Fatalf("failed compact = changed %v, error %v", changed, err)
			}
			if current.Summary != testSessionHandoff() || session.Transcript(current.Messages) != before {
				t.Fatal("failed compaction discarded history")
			}
		})
	}
}

func TestCompactionWithoutNewConversationKeepsHandoff(t *testing.T) {
	writer := &recordingSummaryWriter{summary: "should not be generated"}
	application := &App{Config: config.Default(), summaryWriter: writer}
	current := session.NewSession("named", t.TempDir(), "model")
	persistCompactedContext(current, testSessionHandoff(), "project knowledge")
	changed, err := application.compactSession(t.Context(), current, true)
	if err != nil || changed || writer.called || current.Summary != testSessionHandoff() {
		t.Fatalf("no-op compact = changed %v, called %v, summary %q, error %v", changed, writer.called, current.Summary, err)
	}
}

func TestComposedContextCompactionGeneratesHandoffBelowSummaryGate(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 85
	cfg.SystemPrompt = strings.Repeat("large system prefix ", 80)
	writer := &recordingSummaryWriter{summary: testSessionHandoff()}
	application := &App{Config: cfg, summaryWriter: writer}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Summary = "stale previous context"
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("new task")))
	changed, err := application.compactSession(t.Context(), current, false)
	if err != nil || !changed || !writer.called || current.Summary != writer.summary {
		t.Fatalf("compact = changed %v, called %v, summary %q, error %v", changed, writer.called, current.Summary, err)
	}
}
