package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/changegraph"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

func TestShouldCompactUses85PercentTrigger(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 85
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	for session.ApproxTokensFromMessages(current.CopyMessages()) < 85 {
		current.Append(
			sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("u", 40))),
			sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("a", 40))),
		)
	}
	if !application.shouldCompact(context.Background(), current) {
		t.Fatalf("expected compaction at or above 85%% threshold, got %d tokens", session.ApproxTokensFromMessages(current.CopyMessages()))
	}
}

func TestShouldCompactUsesEstimatedContextTokens(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 85
	cfg.SystemPrompt = strings.Repeat("system context ", 40)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("small prompt")))
	if session.ApproxTokensFromMessages(current.CopyMessages()) >= 85 {
		t.Fatal("test setup expected messages alone to stay below threshold")
	}
	if !application.shouldCompact(context.Background(), current) {
		t.Fatal("expected ctx estimate including system context to trigger compaction")
	}
}

func TestMemorySummaryTriggerUses50PercentThreshold(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.SummaryTriggerPercent = 50
	cfg.SystemPrompt = strings.Repeat("system context ", 40)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("small prompt")))
	if !application.shouldRefreshMemorySummary(context.Background(), current) {
		t.Fatal("expected memory summary refresh to trigger at or above 50% threshold")
	}
}

func TestMemorySummaryTriggerRunsOncePerCycle(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.SummaryTriggerPercent = 50
	cfg.SystemPrompt = strings.Repeat("system context ", 40)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("small prompt")))

	if !application.shouldRefreshMemorySummary(context.Background(), current) {
		t.Fatal("expected first summary refresh to trigger")
	}
	current.Metadata.MemorySummaryCompleted = true
	if application.shouldRefreshMemorySummary(context.Background(), current) {
		t.Fatal("expected summary refresh to stay disabled after completing this threshold cycle")
	}
}

func TestMemorySummaryMaintenanceIsNotPartOfCompaction(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.SummaryTriggerPercent = 1
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("memory ", 100))))

	if !application.shouldRefreshMemorySummary(context.Background(), current) {
		t.Fatal("expected legacy summary threshold helper to identify a large history")
	}
	if current.Metadata.MemorySummaryCompleted {
		t.Fatal("ordinary compaction must not mark memory-summary maintenance complete")
	}
}

func TestCompactTriggerRunsOncePerCycle(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 85
	cfg.SystemPrompt = strings.Repeat("system context ", 80)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("small prompt")))

	if !application.shouldCompact(context.Background(), current) {
		t.Fatal("expected first compaction to trigger")
	}
	current.Metadata.MemoryCompactionCompleted = true
	current.Metadata.MemoryCompactionMessageCount = len(current.Messages)
	if application.shouldCompact(context.Background(), current) {
		t.Fatal("expected compaction to stay disabled until new messages are added")
	}
	current.Append(sdk.NewAssistantMessage(sdk.NewTextBlock("new response after compaction")))
	if !application.shouldCompact(context.Background(), current) {
		t.Fatal("expected compaction to be allowed again after messages grow")
	}
}

func TestMemoryRetrievalBudgetCapsAtTenPercent(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 200_000
	application := &App{Config: cfg}
	if got := application.memoryRetrievalTokenBudget(); got != 20_000 {
		t.Fatalf("expected 20k retrieval budget for 200k context, got %d", got)
	}
	cfg.MaxContextTokens = 1_000_000
	application.Config = cfg
	if got := application.memoryRetrievalTokenBudget(); got != 50_000 {
		t.Fatalf("expected 50k retrieval budget cap for 1M context, got %d", got)
	}
}

type recordingSummaryWriter struct {
	called     bool
	previous   string
	transcript string
	summary    string
	err        error
}

func (w *recordingSummaryWriter) Summarize(_ context.Context, previous, transcript string) (string, error) {
	w.called = true
	w.previous = previous
	w.transcript = transcript
	if w.err != nil {
		return "", w.err
	}
	if w.summary != "" {
		return w.summary, nil
	}
	return "AI-generated session summary", nil
}

func TestCompactSessionFailsWhenAISummaryFails(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 1
	writer := &recordingSummaryWriter{err: errors.New("summary API unavailable")}
	application := &App{Config: cfg, summaryWriter: writer}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(sdk.NewUserMessage(sdk.NewTextBlock("preserve this history")))

	changed, err := application.compactSession(context.Background(), current, false)
	if err == nil || !strings.Contains(err.Error(), "summary API unavailable") {
		t.Fatalf("compactSession() error = %v, want AI summary failure", err)
	}
	if changed {
		t.Fatal("failed AI summary must not report a changed session")
	}
	if len(current.Messages) == 0 {
		t.Fatal("failed AI summary must preserve session history")
	}
}

func TestRefreshSessionSummaryUsesAIAndPersistsResult(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Enabled = true
	cfg.Session.Persist = true
	cfg.Session.Dir = t.TempDir()
	writer := &recordingSummaryWriter{summary: "background AI summary"}
	application := &App{
		Config:        cfg,
		Sessions:      session.NewManager(session.NewFileStore(cfg.Session.Dir), "main"),
		summaryWriter: writer,
	}
	current := session.NewSession("main", t.TempDir(), cfg.Model)
	current.Summary = "previous summary"
	current.Metadata.MemorySummaryCompleted = true
	current.Append(
		sdk.NewUserMessage(sdk.NewTextBlock("finish the session summary feature")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("implemented the AI summary request")),
	)
	if err := application.Sessions.Save(context.Background(), current); err != nil {
		t.Fatalf("save setup session: %v", err)
	}

	if err := application.refreshSessionSummary(context.Background(), "main", current.Metadata.WorkDir); err != nil {
		t.Fatalf("refreshSessionSummary() = %v", err)
	}
	if !writer.called || writer.previous != "previous summary" || !strings.Contains(writer.transcript, "finish the session summary feature") {
		t.Fatalf("summary writer input = called %v, previous %q, transcript %q", writer.called, writer.previous, writer.transcript)
	}
	stored, err := application.Sessions.LoadOrCreate(context.Background(), "main", current.Metadata.WorkDir, cfg.Model)
	if err != nil {
		t.Fatalf("reload summarized session: %v", err)
	}
	if stored.Summary != "background AI summary" || !stored.Metadata.MemorySummaryCompleted {
		t.Fatalf("stored summary state = summary %q, completed %v", stored.Summary, stored.Metadata.MemorySummaryCompleted)
	}
	if len(stored.Messages) != 2 {
		t.Fatalf("background summary must preserve messages, got %d", len(stored.Messages))
	}
}

func TestCompactSessionUsesAISummaryWriter(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 1
	cfg.Memory.CompactionTargetPercent = 1
	writer := &recordingSummaryWriter{}
	application := &App{Config: cfg, summaryWriter: writer}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Summary = "previous session context"
	current.Append(
		sdk.NewUserMessage(sdk.NewTextBlock("implement token validation")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("implemented validation in auth.go")),
	)

	changed, err := application.compactSession(context.Background(), current, false)
	if err != nil {
		t.Fatalf("compactSession() = %v", err)
	}
	if !changed {
		t.Fatal("expected compaction to change session")
	}
	if !writer.called {
		t.Fatal("expected compaction to invoke the AI summary writer")
	}
	if writer.previous != "previous session context" || !strings.Contains(writer.transcript, "implement token validation") {
		t.Fatalf("summary writer input = previous %q, transcript %q", writer.previous, writer.transcript)
	}
	if current.Summary != "AI-generated session summary" {
		t.Fatalf("summary = %q, want AI-generated summary", current.Summary)
	}
}

func TestCompactSessionPersistsOnlyConciseSummary(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 1
	cfg.Memory.CompactionTargetPercent = 1
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(
		sdk.NewUserMessage(sdk.NewTextBlock("implement token validation")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("implemented validation in auth.go")),
	)

	changed, err := application.compactSession(context.Background(), current, false)
	if err != nil {
		t.Fatalf("compactSession() = %v", err)
	}
	if !changed {
		t.Fatal("expected compaction to change session")
	}
	if len(current.Messages) != 1 || !session.IsCompactedSummaryMessage(current.Messages[0]) {
		t.Fatalf("messages after compaction = %#v, want one durable summary message", current.Messages)
	}
	if !strings.HasPrefix(current.Summary, "Recent session state:\n") {
		t.Fatalf("summary = %q, want concise session state", current.Summary)
	}
	if strings.Contains(current.Summary, "Files and Code Sections") || strings.Contains(current.Summary, "Retrieved memory") {
		t.Fatalf("summary should not contain legacy sections: %q", current.Summary)
	}
}

func TestManualCompactSessionPersistsSummaryAndUserMessage(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Enabled = true
	cfg.Session.Persist = true
	cfg.Session.Dir = t.TempDir()
	cfg.MaxContextTokens = 100_000
	graph, err := changegraph.Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatalf("open change graph: %v", err)
	}
	defer func() { _ = graph.Close() }()
	if err := graph.Record(context.Background(), changegraph.Change{
		SessionID:   "main",
		Path:        "internal/session/session.go",
		Language:    "go",
		Description: "add durable compacted context messages",
	}); err != nil {
		t.Fatalf("record change graph event: %v", err)
	}
	application := &App{
		Config:      cfg,
		Sessions:    session.NewManager(session.NewFileStore(cfg.Session.Dir), "main"),
		ChangeGraph: graph,
	}
	current := session.NewSession("main", t.TempDir(), cfg.Model)
	current.Append(
		sdk.NewUserMessage(sdk.NewTextBlock("preserve the active implementation request")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("completed the initial investigation")),
	)
	if err := application.Sessions.Save(context.Background(), current); err != nil {
		t.Fatalf("save setup session: %v", err)
	}

	compacted, changed, err := application.CompactSession(context.Background(), "main", current.Metadata.WorkDir)
	if err != nil {
		t.Fatalf("CompactSession() = %v", err)
	}
	if !changed || compacted == nil {
		t.Fatalf("CompactSession() = (%#v, %v), want changed session", compacted, changed)
	}
	if strings.TrimSpace(compacted.Summary) == "" {
		t.Fatal("manual compaction must persist a non-empty Summary field")
	}
	if len(compacted.Messages) != 2 || !session.IsCompactedSummaryMessage(compacted.Messages[0]) || !session.IsCompactedProjectKnowledgeMessage(compacted.Messages[1]) {
		t.Fatalf("compacted messages = %#v, want durable summary then project knowledge user messages", compacted.Messages)
	}
	if got := sessionSummaryForRequest(compacted); got != "" {
		t.Fatalf("sessionSummaryForRequest() = %q, want no duplicate ephemeral summary", got)
	}

	stored, err := application.Sessions.LoadOrCreate(context.Background(), "main", current.Metadata.WorkDir, cfg.Model)
	if err != nil {
		t.Fatalf("reload compacted session: %v", err)
	}
	if stored.Summary != compacted.Summary {
		t.Fatalf("stored summary = %q, want %q", stored.Summary, compacted.Summary)
	}
	if len(stored.Messages) != 2 || !session.IsCompactedSummaryMessage(stored.Messages[0]) || !session.IsCompactedProjectKnowledgeMessage(stored.Messages[1]) {
		t.Fatalf("stored messages = %#v, want durable summary then project knowledge messages", stored.Messages)
	}
	if got := application.projectKnowledgeForRequest(context.Background(), stored, "next user request"); got != "" {
		t.Fatalf("projectKnowledgeForRequest() = %q, want no duplicate ephemeral project knowledge", got)
	}
	originalSummaryMessage := stored.Messages[0].Content[0].OfText.Text
	stored.Append(
		sdk.NewUserMessage(sdk.NewTextBlock("apply the follow-up change")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("follow-up change completed")),
	)
	if err := application.Sessions.Save(context.Background(), stored); err != nil {
		t.Fatalf("save follow-up session: %v", err)
	}
	updated, changed, err := application.CompactSession(context.Background(), "main", current.Metadata.WorkDir)
	if err != nil {
		t.Fatalf("second CompactSession() = %v", err)
	}
	if !changed || len(updated.Messages) != 2 || !session.IsCompactedSummaryMessage(updated.Messages[0]) || !session.IsCompactedProjectKnowledgeMessage(updated.Messages[1]) {
		t.Fatalf("updated messages = %#v, want replacement summary then project knowledge", updated.Messages)
	}
	if got := updated.Messages[0].Content[0].OfText.Text; got == originalSummaryMessage || !strings.Contains(got, "follow-up change completed") {
		t.Fatalf("replacement summary = %q, want new follow-up state", got)
	}
}

func TestCompactSessionCreatesSummaryWhenComposedContextTriggers(t *testing.T) {
	cfg := config.Default()
	cfg.MaxContextTokens = 100
	cfg.Memory.CompactionTriggerPercent = 85
	cfg.Memory.CompactionTargetPercent = 50
	cfg.SystemPrompt = strings.Repeat("system context ", 80)
	application := &App{Config: cfg}
	current := session.NewSession("named", t.TempDir(), cfg.Model)
	current.Append(
		sdk.NewUserMessage(sdk.NewTextBlock("preserve this user request")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("preserve this completed outcome")),
	)
	if session.ApproxTokensFromMessages(current.Messages) >= application.compactionTriggerTokens() {
		t.Fatal("test setup requires messages alone below the trigger")
	}

	changed, err := application.compactSession(context.Background(), current, false)
	if err != nil {
		t.Fatalf("compactSession() = %v", err)
	}
	if !changed {
		t.Fatal("expected composed request threshold to create a session summary")
	}
	if len(current.Messages) != 1 || !session.IsCompactedSummaryMessage(current.Messages[0]) {
		t.Fatalf("messages after compaction = %d, want one durable summary message", len(current.Messages))
	}
	for _, want := range []string{"preserve this user request", "preserve this completed outcome"} {
		if !strings.Contains(current.Summary, want) {
			t.Fatalf("summary = %q, want %q", current.Summary, want)
		}
	}
}

func TestConciseSessionSummaryKeepsUserIntentWithoutRolePrefix(t *testing.T) {
	summary := conciseSessionSummary("user: Continue custom provider setup\nassistant: I will continue.", "")
	if !strings.Contains(summary, "Continue custom provider setup") {
		t.Fatalf("summary = %q, want user intent", summary)
	}
	if strings.Contains(strings.ToLower(summary), "user:") {
		t.Fatalf("summary must not retain a user role prefix: %q", summary)
	}
}

func TestConciseSessionSummaryDropsViewPaginationNoise(t *testing.T) {
	transcript := strings.Join([]string{
		"user: Fix session compaction",
		"assistant: Investigated the compaction flow.",
		"user: [tool result]",
		"(File has 1038 more lines. Use 'offset' parameter to read beyond line 260)",
		"assistant: Session compaction now preserves the prior conversation.",
	}, "\n")
	summary := conciseSessionSummary(transcript, "")
	for _, want := range []string{"Fix session compaction", "Session compaction now preserves the prior conversation."} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary = %q, want %q", summary, want)
		}
	}
	if strings.Contains(strings.ToLower(summary), "file has") || strings.Contains(strings.ToLower(summary), "offset") {
		t.Fatalf("summary must not retain View pagination noise: %q", summary)
	}
}

func TestSanitizeLoadedSessionSummaryRemovesViewPaginationNoise(t *testing.T) {
	polluted := "Recent session state:\n- (File has 1038 more lines. Use 'offset' parameter to read beyond line 260)"
	if got := sanitizeLoadedSessionSummary(polluted); got != "" {
		t.Fatalf("sanitized pagination-only summary = %q, want empty", got)
	}
}

func TestSanitizeLoadedSessionSummaryRemovesEmptyRecentSessionState(t *testing.T) {
	if got := sanitizeLoadedSessionSummary("Recent session state:"); got != "" {
		t.Fatalf("sanitized empty summary = %q, want empty", got)
	}
}

func TestSanitizeLoadedSessionSummaryRemovesPlaceholderOutline(t *testing.T) {
	polluted := strings.Join([]string{
		"文件变更图上下文",
		"旧 session 对话压缩结果",
		"用户最新 prompt",
		"go test ./internal/app ./internal/engine ./internal/session ./cmd/solcode",
		"go build -o solcode.exe ./cmd/solcode",
	}, "\n")
	if got := sanitizeLoadedSessionSummary(polluted); got != "" {
		t.Fatalf("sanitized placeholder summary = %q, want empty", got)
	}
}

func TestNewSessionMemoryRetrievalGate(t *testing.T) {
	current := session.NewSession("named", t.TempDir(), "test-model")
	allowed := true
	current.Metadata.CrossSessionMemory = &allowed
	if !shouldRetrieveNewSessionMemory(current, true) {
		t.Fatal("expected a new opted-in session to retrieve cross-session memory")
	}
	if shouldRetrieveNewSessionMemory(current, false) {
		t.Fatal("did not expect an established session to retrieve memory")
	}
	current.Metadata.MemoryBootstrapPending = true
	if !shouldRetrieveNewSessionMemory(current, false) {
		t.Fatal("expected pending bootstrap to retrieve memory once")
	}
	denied := false
	current.Metadata.CrossSessionMemory = &denied
	if shouldRetrieveNewSessionMemory(current, true) {
		t.Fatal("did not expect a denied session to retrieve memory")
	}
}

func TestCompactUses50PercentTarget(t *testing.T) {
	messages := []sdk.MessageParam{}
	for i := 0; i < 12; i++ {
		messages = append(messages,
			sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("user turn ", 12))),
			sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("assistant turn ", 12))),
		)
	}
	before := session.ApproxTokensFromMessages(messages)
	result, err := session.Compact(context.Background(), "previous", messages, nil, session.CompactOptions{
		MaxRecentTurns:         20,
		SummaryThresholdTokens: 1,
		TargetTokens:           before / 2,
	})
	if err != nil {
		t.Fatalf("Compact() = %v", err)
	}
	if !result.Changed {
		t.Fatal("expected compaction to change session")
	}
	retained := session.ApproxTokensFromMessages(result.Messages)
	if retained >= before {
		t.Fatalf("expected compaction to reduce retained messages, before=%d after=%d", before, retained)
	}
	if retained > before*3/5 {
		t.Fatalf("expected retained messages to be pushed down toward the 50%% target, before=%d after=%d", before, retained)
	}
	if strings.TrimSpace(result.CompactedTranscript) == "" {
		t.Fatal("expected compacted transcript to be populated")
	}
}

func TestSanitizeLoadedSessionStateRemovesPollutedSummary(t *testing.T) {
	current := session.NewSession("named", t.TempDir(), "test-model")
	current.Summary = strings.Join([]string{
		"1. Primary Request and Intent:",
		"- code-change, files · Compacted session file modifications: internal/app/app.go: edited (replaced old behavior -> new behavior); internal/engine/engine.go: edited (replaced foo -> bar).",
		"- tool-usage, compaction · Compacted session tool usage: Edit, Bash.",
		"340| current.ReplaceMessages(session.StripEphemeralContextMessages(current.CopyMessages()))",
		"```go",
		`{"old_string":"x","new_string":"y"}`,
	}, "\n")

	changed := sanitizeLoadedSessionState(current)
	if !changed {
		t.Fatal("expected polluted summary to be sanitized")
	}
	for _, unwanted := range []string{"replaced old behavior", "Compacted session tool usage", "340|", "```go", `"old_string"`, `"new_string"`} {
		if strings.Contains(current.Summary, unwanted) {
			t.Fatalf("did not expect %q in sanitized summary: %s", unwanted, current.Summary)
		}
	}
	for _, want := range []string{"Compacted session file modifications:", "internal/app/app.go: edited", "internal/engine/engine.go: edited"} {
		if !strings.Contains(current.Summary, want) {
			t.Fatalf("expected %q in sanitized summary: %s", want, current.Summary)
		}
	}
}

func TestSanitizeLoadedSessionStateRecompactsExactPollutedSessionSummarySample(t *testing.T) {
	current := session.NewSession("named", t.TempDir(), "test-model")
	current.Summary = strings.Join([]string{
		"Session summary:",
		"user: 继续",
		"var b strings.Builder",
		"+ \tvar b strings.Builder",
		"assistant: 我继续直接收尾：先把 `app.go` 的 build 错修掉，再把“加载旧 session 时自动去污 summary”接上，同时更新失效测试。",
		"assistant: 我继续直接修，先把 **build/test 断点** 和 **旧 session summary 去污入口** 一起收掉。",
		"for _, want := range []string{\"internal/app/app.go: edited\", \"old behavior\", \"new behavior\", \"go test ./internal/app ./internal/session\", \"Edit\", \"Bash\"} {",
		"+ \tfor _, want := range []string{\"internal/app/app.go: edited\", \"targeted replacement\", \"go test ./internal/app ./internal/session\", \"Edit\", \"Bash\"} {",
		"`{\"command\":\"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go\"}`",
		"Compacted session file modifications: internal/anthropic/messages.go: edited; internal/app/app.go: edited; internal/app/app.go: edited; internal/app/app.go: edited; internal/engine/engine.go: edited; internal/engine/engine.go: edited.",
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"Compacted session validation/build commands run: \" + strings.Join(cleaned, \"; \") + \".\".",
		"files := dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(priorHints, []string{\"files\", \"code sections\", \"file modifications\"})...))",
		"item.ID = strings.TrimSuffix(entry.Name(), \".json\")",
		"func (m *Manager) RememberExtracted(ctx context.Context, input ExtractionInput) ([]Item, error) {",
		"assistant: 这份 `Session summary` 说明还有一层摘要级噪声没截干净：",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"[→] Tighten prior summary sanitization drop diff/code/todo noise (in_progress)",
		"@@ -810,7 +810,9 @@",
		"pending := summarizePending(append(lines, priorHints...), compactPreviousSummary(previous))",
		"currentWork = []string{primary}",
		"internal/memory/sanitize.go",
		"internal/memory/memory.go",
		"internal/memory/manager.go",
		"internal/memory/sanitize_test.go",
		"internal/app/app.go",
		"unit_tests/memory_summary_test.go",
	}, "\n")

	changed := sanitizeLoadedSessionState(current)
	if !changed {
		t.Fatal("expected exact polluted summary sample to be sanitized")
	}
	for _, want := range []string{
		"Compacted session file modifications:",
		"internal/anthropic/messages.go: edited",
		"internal/app/app.go: edited",
		"internal/engine/engine.go: edited",
		"internal/memory/sanitize.go",
		"internal/memory/memory.go",
		"internal/memory/manager.go",
		"internal/memory/sanitize_test.go",
		"internal/app/app.go",
		"unit_tests/memory_summary_test.go",
	} {
		if !strings.Contains(current.Summary, want) {
			t.Fatalf("expected %q in sanitized summary: %s", want, current.Summary)
		}
	}
	for _, unwanted := range []string{
		"Session summary:",
		"user: 继续",
		"var b strings.Builder",
		"assistant: 我继续",
		`"command":"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go"`,
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"files := dedupeSummaryLines",
		"item.ID = strings.TrimSuffix",
		"func (m *Manager) RememberExtracted(ctx context.Context, input ExtractionInput) ([]Item, error) {",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"[→] Tighten prior summary sanitization drop diff/code/todo noise (in_progress)",
		"@@ -810,7 +810,9 @@",
		"currentWork = []string{primary}",
	} {
		if strings.Contains(current.Summary, unwanted) {
			t.Fatalf("did not expect %q in sanitized summary: %s", unwanted, current.Summary)
		}
	}
	if strings.Contains(current.Summary, "internal/app/app.go: edited; internal/app/app.go: edited") {
		t.Fatalf("expected duplicated app.go modification entries to collapse: %s", current.Summary)
	}
	if strings.Contains(current.Summary, "internal/engine/engine.go: edited; internal/engine/engine.go: edited") {
		t.Fatalf("expected duplicated engine.go modification entries to collapse: %s", current.Summary)
	}
}
