package unit_tests

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/tool"
)

// The default system prompt must tell the model that the memory tools exist and
// when to reach for them; otherwise the tools are registered but never used.
func TestDefaultSystemPromptDocumentsMemoryTools(t *testing.T) {
	builder := engine.ContextBuilder{}
	req := builder.Build(engine.BuildRequest{
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hello"))},
	})
	for _, want := range []string{
		"# Memory",
		"WriteMemory",
		"search_context",
		"WriteSessionMemory",
		"source=\"session_memory\"",
		".solcode/solcode.md",
		"not interchangeable",
		"Core:",
		"Archival:",
		"Recall:",
		"cross-session memory",
		"local memory organizer",
	} {
		if !strings.Contains(req.System, want) {
			t.Fatalf("expected system prompt to document memory tools with %q, got %q", want, req.System)
		}
	}
}

func TestSessionMemoryToolsDistinguishFromGlobalMemory(t *testing.T) {
	writeDesc := tool.NewWriteSessionMemoryTool(nil).Description()
	for _, want := range []string{
		"session log",
		"WriteMemory",
		"checkpoint turn",
		"session id",
		".solcode/solcode.md",
	} {
		if !strings.Contains(writeDesc, want) {
			t.Fatalf("WriteSessionMemory description missing %q: %s", want, writeDesc)
		}
	}
	readDesc := tool.NewReadSessionMemoryTool(nil).Description()
	for _, want := range []string{
		"ReadMemory",
		"chronological log",
		"newest first",
		".solcode/solcode.md",
		"current session",
	} {
		if !strings.Contains(readDesc, want) {
			t.Fatalf("ReadSessionMemory description missing %q: %s", want, readDesc)
		}
	}
}

func TestWriteMemoryDescriptionCoversWhenAndWhatToSave(t *testing.T) {
	desc := tool.NewWriteMemoryTool(nil).Description()
	for _, want := range []string{
		"durable",               // what qualifies
		"normal task-lifecycle", // proactive use
		"before your final response",
		"one to three focused entries",
		"TodoWrite",       // where transient state belongs instead
		"secrets",         // rejected content
		"checkpoint turn", // write tags the current turn
		"search_context",  // how entries come back
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("WriteMemory description missing %q: %s", want, desc)
		}
	}
}

func TestSearchContextDescriptionCoversWhenToLookUp(t *testing.T) {
	desc := tool.NewSearchContextTool(nil, nil).Description()
	for _, want := range []string{
		"WriteMemory",    // pairing
		"cross-session",  // visibility rule
		"other-session",  // result labeling
		"trust the code", // conflict resolution
	} {
		if !strings.Contains(strings.ToLower(desc), strings.ToLower(want)) {
			t.Fatalf("search_context description missing %q: %s", want, desc)
		}
	}
}
