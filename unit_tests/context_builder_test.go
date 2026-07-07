package unit_tests

import (
	"strings"
	"testing"

	"github.com/solosw/codeplus-agent/internal/engine"
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
		"You are codeplus-agent, an interactive CLI-based coding agent that helps with software engineering tasks.",
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
	idxDefault := strings.Index(req.System, "You are codeplus-agent")
	idxTools := strings.Index(req.System, "Tool usage:")
	idxSkills := strings.Index(req.System, "Skills:")
	if !(idxUser >= 0 && idxDefault > idxUser && idxTools > idxDefault && idxSkills > idxTools) {
		t.Fatalf("unexpected system prompt order: %q", req.System)
	}
	if len(req.Messages) < 2 {
		t.Fatalf("expected context messages to be prepended, got %d messages", len(req.Messages))
	}
	rendered := req.Messages[0].Content
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
		t.Fatalf("expected session summary in context messages, got %v", rendered)
	}
	if !foundMemory {
		t.Fatalf("expected retrieved memory in context messages, got %v", rendered)
	}
}
