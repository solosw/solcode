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
		"ReadMemory",
		"WriteSessionMemory",
		"ReadSessionMemory",
		".solcode/solcode.md",
		"not interchangeable",
		"normal task-lifecycle action",
		"before the final response",
		"one to three entries",
		"cross-session memory",
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
		"TodoWrite",      // where transient state belongs instead
		"secrets",        // rejected content
		"near-duplicate", // merge behavior
		"ReadMemory",     // how entries come back
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("WriteMemory description missing %q: %s", want, desc)
		}
	}
}

func TestReadMemoryDescriptionCoversWhenToLookUp(t *testing.T) {
	desc := tool.NewReadMemoryTool(nil).Description()
	for _, want := range []string{
		"WriteMemory",    // pairing
		"cross-session",  // visibility rule
		"other-session",  // result labeling
		"trust the code", // conflict resolution
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("ReadMemory description missing %q: %s", want, desc)
		}
	}
}
