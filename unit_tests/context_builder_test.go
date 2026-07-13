package unit_tests

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/engine"
)

func TestContextBuilderSystemPromptIsStable(t *testing.T) {
	builder := engine.ContextBuilder{SystemPrompt: "user system", SkillNames: []string{"review", "verify"}}
	req := builder.Build(engine.BuildRequest{
		WorkDir:        "C:/work",
		SessionSummary: "User is building session memory.",
		MemoryContext: []engine.ContextItem{
			{Title: "Project", Content: "Use persistent main session."},
		},
	})
	for _, want := range []string{
		"user system",
		"You are solcode, an interactive CLI-based coding agent that helps with software engineering tasks.",
		"Tool usage:",
		"Skills:",
		"Skills are reusable markdown workflows loaded from the configured skills directories.",
		"Available skills: review, verify",
		"Working directory: C:/work",
	} {
		if !strings.Contains(req.System, want) {
			t.Fatalf("expected system prompt to contain %q, got %q", want, req.System)
		}
	}
	for _, mustNot := range []string{"Session summary:", "Retrieved memory:", "User is building session memory."} {
		if strings.Contains(req.System, mustNot) {
			t.Fatalf("system prompt must NOT contain dynamic context %q (breaks cache), got %q", mustNot, req.System)
		}
	}
	idxUser := strings.Index(req.System, "user system")
	idxDefault := strings.Index(req.System, "You are solcode")
	idxTools := strings.Index(req.System, "Tool usage:")
	idxSkills := strings.Index(req.System, "Skills:")
	if !(idxUser >= 0 && idxDefault > idxUser && idxTools > idxDefault && idxSkills > idxTools) {
		t.Fatalf("unexpected system prompt order: %q", req.System)
	}
	if len(req.Messages) < 2 {
		t.Fatalf("expected context messages to be appended, got %d messages", len(req.Messages))
	}
	rendered := req.Messages[len(req.Messages)-2].Content
	foundSummary := false
	foundMemory := false
	for _, block := range rendered {
		if block.OfText != nil {
			if strings.Contains(block.OfText.Text, "User is building session memory.") {
				foundSummary = true
			}
			if strings.Contains(block.OfText.Text, "Project: Use persistent main session.") {
				foundMemory = true
			}
		}
	}
	if !foundSummary {
		t.Fatalf("expected session summary in appended context messages, got %v", rendered)
	}
	if !foundMemory {
		t.Fatalf("expected retrieved memory in appended context messages, got %v", rendered)
	}
	if req.Messages[len(req.Messages)-1].Content[0].OfText == nil || !strings.Contains(req.Messages[len(req.Messages)-1].Content[0].OfText.Text, "keep this context in mind") {
		t.Fatalf("expected assistant ack at tail, got %#v", req.Messages[len(req.Messages)-1])
	}
}

func TestContextBuilderInjectsProjectKnowledgeOutsideSystemPrompt(t *testing.T) {
	builder := engine.ContextBuilder{}
	req := builder.Build(engine.BuildRequest{
		Messages:         []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("continue"))},
		ProjectKnowledge: "## Active tasks\n- [in_progress] Finish graph\n\n## Recent tracked changes\n- api.go — add endpoint",
	})
	if strings.Contains(req.System, "Finish graph") || strings.Contains(req.System, "add endpoint") {
		t.Fatalf("project knowledge must not enter stable system prompt: %q", req.System)
	}
	if len(req.Messages) < 2 {
		t.Fatalf("expected injected project knowledge messages, got %#v", req.Messages)
	}
	var rendered strings.Builder
	for _, message := range req.Messages {
		for _, block := range message.Content {
			if block.OfText != nil {
				rendered.WriteString(block.OfText.Text)
			}
		}
	}
	for _, want := range []string{"Finish graph", "add endpoint"} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("project knowledge message missing %q: %q", want, rendered.String())
		}
	}
}

func TestContextBuilderSanitizesPollutedSessionSummaryBlock(t *testing.T) {
	builder := engine.ContextBuilder{}
	req := builder.Build(engine.BuildRequest{
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("继续"))},
		SessionSummary: strings.Join([]string{
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
			"Compacted session validation/build commands run: \"):]).",
			"+\t\t\treturn \"Compacted session validation/build commands run.\"",
			"Compacted session validation/build commands run: \" + strings.Join(cleaned, \"; \") + \".\".",
			"+\t\t\"+ \tvar output strings.Builder\",",
			"+\t\t\"if strings.Contains(lower, \\\"gofmt\\\") && strings.Contains(lower, \\\" -w\\\") {\",",
			"我先 `gofmt`，再用 **逐条精确测试名** 的方式重跑，避免 shell 引号问题。",
			"\"+ \tvar output strings.Builder\",",
			"\"if strings.Contains(lower, \\\"gofmt\\\") && strings.Contains(lower, \\\" -w\\\") {\",",
			"internal/memory/sanitize.go",
			"internal/memory/memory.go",
			"internal/memory/manager.go",
			"internal/memory/sanitize_test.go",
			"internal/app/app.go",
			"unit_tests/memory_summary_test.go",
			"files := dedupeSummaryLines(append(append(priorityFiles, toolFileHints...), extractRelevantPriorHints(priorHints, []string{\"files\", \"code sections\", \"file modifications\"})...))",
			"item.ID = strings.TrimSuffix(entry.Name(), \".json\")",
			"func (m *Manager) RememberExtracted(ctx context.Context, input ExtractionInput) ([]Item, error) {",
			"[ ] Add regression tests stored memory self-healing retrieval (pending)",
			"@@ -810,7 +810,9 @@",
			"currentWork = []string{primary}",
		}, "\n"),
	})
	if len(req.Messages) == 0 || req.Messages[0].Content[0].OfText == nil {
		t.Fatalf("expected injected context message, got %#v", req.Messages)
	}
	text := req.Messages[0].Content[0].OfText.Text
	for _, want := range []string{
		"Session summary:",
		"Compacted session file modifications: internal/anthropic/messages.go: edited; internal/app/app.go: edited; internal/engine/engine.go: edited.",
		"internal/memory/sanitize.go",
		"internal/memory/memory.go",
		"internal/memory/manager.go",
		"internal/memory/sanitize_test.go",
		"internal/app/app.go",
		"unit_tests/memory_summary_test.go",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected sanitized context block to contain %q, got:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{
		"user: 继续",
		"var b strings.Builder",
		"assistant: 我继续",
		"Compacted session validation/build commands run: \"); idx >= 0 {.",
		"Compacted session validation/build commands run: \"):]).",
		"return \"Compacted session validation/build commands run.\"",
		"files := dedupeSummaryLines",
		"item.ID = strings.TrimSuffix",
		"[ ] Add regression tests stored memory self-healing retrieval (pending)",
		"@@ -810,7 +810,9 @@",
		"currentWork = []string{primary}",
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("did not expect polluted session-summary content %q, got:\n%s", unwanted, text)
		}
	}
}
