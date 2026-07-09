package memory

import (
	"context"
	"strings"
	"testing"
)

func TestExtractToolTraceMemories(t *testing.T) {
	input := ExtractionInput{Transcript: strings.Join([]string{
		`assistant: [tool use: Edit]`,
		`{"file_path":"internal/app/app.go","old_string":"old behavior","new_string":"new behavior"}`,
		`assistant: [tool use: Bash]`,
		`{"command":"go test ./internal/app ./internal/session"}`,
	}, "\n")}
	judgements := extractToolTraceMemories(input)
	if len(judgements) != 3 {
		t.Fatalf("expected 3 deterministic memories, got %d: %#v", len(judgements), judgements)
	}
	joined := judgements[0].CanonicalText + "\n" + judgements[1].CanonicalText + "\n" + judgements[2].CanonicalText
	for _, want := range []string{"internal/app/app.go: edited", "targeted replacement", "go test ./internal/app ./internal/session", "Edit", "Bash"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in deterministic memories: %s", want, joined)
		}
	}
	for _, unwanted := range []string{"old behavior", "new behavior"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("did not expect verbose replacement snippet %q in deterministic memories: %s", unwanted, joined)
		}
	}
	if !strings.Contains(judgements[0].CanonicalText, "file modifications") {
		t.Fatalf("expected file modifications memory first, got %#v", judgements)
	}
}

func TestExtractToolTraceMemoriesPrioritizesPerFileModifications(t *testing.T) {
	input := ExtractionInput{CompactedTranscript: strings.Join([]string{
		`assistant: [tool use: Write]`,
		`{"file_path":"internal/session/compactor.go","content":"new compactor implementation"}`,
		`assistant: [tool use: Patch]`,
		`{"file_path":"internal/memory/anthropic_extractor.go","patch_text":"+prompt requires per-file modification summaries"}`,
		`assistant: [tool use: Bash]`,
		`{"command":"gofmt -w internal/session/compactor.go internal/memory/anthropic_extractor.go"}`,
	}, "\n")}
	judgements := extractToolTraceMemories(input)
	if len(judgements) == 0 {
		t.Fatal("expected memories")
	}
	first := judgements[0].CanonicalText
	for _, want := range []string{
		"internal/session/compactor.go: wrote/overwrote file",
		"internal/memory/anthropic_extractor.go: applied patch",
		"formatted files via bash command",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("expected %q in first file modification memory: %s", want, first)
		}
	}
	if strings.Contains(first, "tools used") {
		t.Fatalf("file modification memory should not be a tool list: %s", first)
	}
}

func TestRememberExtractedStoresToolTraceWithoutExtractor(t *testing.T) {
	manager := NewManagerWithExtractor(NewFileStore(t.TempDir()), DefaultGate{}, nil, nil)
	items, err := manager.RememberExtracted(context.Background(), ExtractionInput{
		SourceSessionID: "main",
		WorkDir:         t.TempDir(),
		Transcript: `[tool use: Write]
{"file_path":"internal/memory/tool_trace.go","content":"updated extraction"}`,
	})
	if err != nil {
		t.Fatalf("RememberExtracted() error = %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected deterministic tool trace memories to be stored")
	}
	found := false
	for _, item := range items {
		if containsString(item.Tags, "modifications") && strings.Contains(item.Text, "internal/memory/tool_trace.go: wrote/overwrote file") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected concrete file modification memory in stored memories: %#v", items)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
