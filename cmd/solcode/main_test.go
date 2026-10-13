package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	appcore "github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	contextmessage "github.com/solosw/solcode/internal/message"
	"github.com/solosw/solcode/internal/session"
)

func TestNewAppWithSessionFallbackCreatesFreshSession(t *testing.T) {
	cfg := config.Default()
	cfg.APIKey = "test"
	cfg.Session.Enabled = true
	cfg.Session.Persist = true
	cfg.Session.Dir = t.TempDir()
	cfg.Session.DefaultSession = "main"

	first, _, err := newAppWithSessionFallback(cfg)
	if err != nil {
		t.Fatalf("first app: %v", err)
	}
	defer first.Close()

	second, secondCfg, err := newAppWithSessionFallback(cfg)
	if err != nil {
		t.Fatalf("fallback app: %v", err)
	}
	defer second.Close()
	if secondCfg.Session.DefaultSession == "main" {
		t.Fatalf("fallback session = %q, want a new session", secondCfg.Session.DefaultSession)
	}
	if !strings.HasPrefix(secondCfg.Session.DefaultSession, "session-") {
		t.Fatalf("fallback session = %q, want session-*", secondCfg.Session.DefaultSession)
	}
}

func TestPersistsPermissionModeAndEffort(t *testing.T) {
	persistencePath := filepath.Join(t.TempDir(), "settings.local.json")
	if err := config.SaveLocalOverrides(persistencePath, map[string]any{
		"permission_mode": "plan",
		"permissions":     map[string]any{"mode": "plan"},
		"effort":          "high",
	}); err != nil {
		t.Fatalf("save local overrides: %v", err)
	}

	data, err := os.ReadFile(persistencePath)
	if err != nil {
		t.Fatalf("read local overrides: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode local overrides: %v", err)
	}
	if got["permission_mode"] != "plan" || got["effort"] != "high" {
		t.Fatalf("persisted settings = %#v", got)
	}
	permissions, ok := got["permissions"].(map[string]any)
	if !ok || permissions["mode"] != "plan" {
		t.Fatalf("persisted permissions = %#v", got["permissions"])
	}
}

func TestApplyWorkDirOverrideUpdatesDefaultStateDirs(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = filepath.Join(t.TempDir(), "old")
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() = %v", err)
	}
	newWorkDir := filepath.Join(t.TempDir(), "new")
	applyWorkDirOverride(&cfg, newWorkDir)

	if cfg.Session.Dir != config.DefaultSessionDir(newWorkDir) {
		t.Fatalf("Session.Dir = %q, want %q", cfg.Session.Dir, config.DefaultSessionDir(newWorkDir))
	}
	if cfg.Memory.Dir != config.DefaultMemoryDir(newWorkDir) {
		t.Fatalf("Memory.Dir = %q, want %q", cfg.Memory.Dir, config.DefaultMemoryDir(newWorkDir))
	}
}

func TestApplyWorkDirOverridePreservesCustomStateDirs(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = filepath.Join(t.TempDir(), "old")
	customSessionDir := filepath.Join(t.TempDir(), "sessions")
	customMemoryDir := filepath.Join(t.TempDir(), "memories")
	cfg.Session.Dir = customSessionDir
	cfg.Memory.Dir = customMemoryDir
	applyWorkDirOverride(&cfg, filepath.Join(t.TempDir(), "new"))

	if cfg.Session.Dir != customSessionDir {
		t.Fatalf("Session.Dir = %q, want custom path %q", cfg.Session.Dir, customSessionDir)
	}
	if cfg.Memory.Dir != customMemoryDir {
		t.Fatalf("Memory.Dir = %q, want custom path %q", cfg.Memory.Dir, customMemoryDir)
	}
}

func TestConversationContextWithoutTimeoutHasNoDeadline(t *testing.T) {
	ctx, cancel := conversationContext(0)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("timeout=0 should create a context without a deadline")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("new context error = %v", err)
	}
}

func TestConversationContextWithTimeoutHasDeadline(t *testing.T) {
	ctx, cancel := conversationContext(time.Minute)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("positive timeout should create a context with a deadline")
	}
}

func TestChatMessagesFromSessionUsesPersistedMessageTimes(t *testing.T) {
	first := time.Date(2024, time.January, 2, 3, 4, 0, 0, time.UTC)
	second := first.Add(5 * time.Minute)
	s := session.NewSession("main", "", "")
	s.Messages = []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("first message")),
		sdk.NewAssistantMessage(sdk.NewTextBlock("second message")),
	}
	s.MessageTimestamps = []time.Time{first, second}

	messages := chatMessagesFromSession(s)
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want loaded-session notice plus two transcript messages", len(messages))
	}
	if messages[1].Role != "user" || !messages[1].TimeStamp.Equal(first) {
		t.Fatalf("first transcript message = %#v, want user at %s", messages[1], first)
	}
	if messages[2].Role != "assistant" || !messages[2].TimeStamp.Equal(second) {
		t.Fatalf("second transcript message = %#v, want assistant at %s", messages[2], second)
	}
}

func TestChatMessagesFromSessionFoldsPersistedPasteForDisplay(t *testing.T) {
	label := "[Pasted text #1 · 356 lines]"
	body := strings.Repeat("long pasted line\n", 356)
	content := label + "\n\n--- Begin " + label + " ---\n" + body + "--- End " + label + " ---"
	s := session.NewSession("main", "", "")
	s.Messages = []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(content))}

	messages := chatMessagesFromSession(s)
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want loaded-session notice plus user message", len(messages))
	}
	message := messages[1]
	if !strings.Contains(message.Content, body) {
		t.Fatal("expected persisted paste body to remain available as message content")
	}
	if strings.TrimSpace(message.DisplayContent) != label {
		t.Fatalf("display content = %q, want compact paste label %q", message.DisplayContent, label)
	}
}

func TestLoadSanitizedSessionRewritesPollutedSummary(t *testing.T) {
	ctx := context.Background()
	store := session.NewFileStore(t.TempDir())
	manager := session.NewManager(store, "main")
	current := session.NewSession("main", t.TempDir(), "test-model")
	current.Summary = strings.Join([]string{
		"Session summary:",
		"user: 继续",
		"var b strings.Builder",
		"assistant: 我继续直接修，先把 **build/test 断点** 和 **旧 session summary 去污入口** 一起收掉。",
		"Compacted session file modifications: internal/anthropic/messages.go: edited; internal/app/app.go: edited; internal/app/app.go: edited; internal/engine/engine.go: edited; internal/engine/engine.go: edited.",
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"files := dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(priorHints, []string{\"files\", \"code sections\", \"file modifications\"})...))",
		"item.ID = strings.TrimSuffix(entry.Name(), \".json\")",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"@@ -810,7 +810,9 @@",
		"currentWork = []string{primary}",
		"internal/memory/sanitize.go",
		"internal/memory/memory.go",
		"internal/memory/manager.go",
		"internal/memory/sanitize_test.go",
		"internal/app/app.go",
		"unit_tests/memory_summary_test.go",
	}, "\n")
	if err := manager.Save(ctx, current); err != nil {
		t.Fatalf("save polluted session: %v", err)
	}

	application := &appcore.App{Sessions: manager}
	cfg := config.Default()
	cfg.WorkDir = current.Metadata.WorkDir
	cfg.Model = "test-model"

	loaded, err := loadSanitizedSession(ctx, application, "main", cfg)
	if err != nil {
		t.Fatalf("loadSanitizedSession: %v", err)
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
		if !strings.Contains(loaded.Summary, want) {
			t.Fatalf("expected sanitized loaded summary to contain %q, got:\n%s", want, loaded.Summary)
		}
	}
	for _, unwanted := range []string{
		"Session summary:",
		"user: 继续",
		"var b strings.Builder",
		"assistant: 我继续",
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"files := dedupeSummaryLines",
		"item.ID = strings.TrimSuffix",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"@@ -810,7 +810,9 @@",
		"currentWork = []string{primary}",
	} {
		if strings.Contains(loaded.Summary, unwanted) {
			t.Fatalf("did not expect %q in sanitized loaded summary:\n%s", unwanted, loaded.Summary)
		}
	}
	if strings.Contains(loaded.Summary, "internal/app/app.go: edited; internal/app/app.go: edited") {
		t.Fatalf("expected duplicated app.go entries to collapse, got:\n%s", loaded.Summary)
	}

	reloaded, err := manager.LoadOrCreate(ctx, "main", current.Metadata.WorkDir, "test-model")
	if err != nil {
		t.Fatalf("reload persisted session: %v", err)
	}
	if reloaded.Summary != loaded.Summary {
		t.Fatalf("expected sanitized summary to persist to disk\nloaded:\n%s\nreloaded:\n%s", loaded.Summary, reloaded.Summary)
	}
}

func TestChatMessagesFromSessionHidesDurableDynamicContext(t *testing.T) {
	s := session.NewSession("main", "", "")
	s.Messages = []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("real task")),
		sdk.NewUserMessage(sdk.NewTextBlock(contextmessage.DynamicContextText("working-set", "internal metadata"))),
		sdk.NewAssistantMessage(sdk.NewTextBlock("real answer")),
	}
	messages := chatMessagesFromSession(s)
	if len(messages) != 3 || messages[1].Content != "real task" || messages[2].Content != "real answer" {
		t.Fatalf("internal overlays leaked into reloaded TUI history: %+v", messages)
	}
}
