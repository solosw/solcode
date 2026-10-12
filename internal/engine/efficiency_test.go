package engine

import (
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func viewInput(path string) []byte {
	return []byte(`{"path":"` + path + `"}`)
}

func TestEfficiencyHintFlagsRepeatedReads(t *testing.T) {
	c := NewRequestEfficiencyController()
	c.BeginTurn(0)
	c.ObserveCall(tool.ViewToolName, viewInput("a.go"), ToolResult{})
	c.ObserveCall(tool.ViewToolName, viewInput("a.go"), ToolResult{})
	if hint := c.Hint(); !strings.Contains(hint, "a.go") {
		t.Fatalf("repeat read not surfaced: %q", hint)
	}
}

func TestEfficiencyHintFlagsRepeatedErrors(t *testing.T) {
	c := NewRequestEfficiencyController()
	failure := ToolResult{IsError: true, Content: tool.ErrorResult("permission denied")}
	c.ObserveCall(tool.BashToolName, []byte(`{"command":"x"}`), failure)
	c.ObserveCall(tool.BashToolName, []byte(`{"command":"x"}`), failure)
	if hint := c.Hint(); !strings.Contains(hint, "already seen") {
		t.Fatalf("repeat error not surfaced: %q", hint)
	}
}

func TestEfficiencyIdleTurnHint(t *testing.T) {
	c := NewRequestEfficiencyController()
	// Three turns with no progress should produce an idle hint.
	c.BeginTurn(0)
	c.BeginTurn(1)
	c.BeginTurn(2)
	c.BeginTurn(3)
	if hint := c.Hint(); !strings.Contains(hint, "without new") {
		t.Fatalf("idle turns not surfaced: %q", hint)
	}
}

func TestEfficiencyProgressClearsIdle(t *testing.T) {
	c := NewRequestEfficiencyController()
	c.BeginTurn(0)
	c.BeginTurn(1)
	// A successful edit is progress and resets the idle window.
	c.ObserveCall(tool.EditToolName, []byte(`{"path":"a.go","old_string":"a","new_string":"b"}`), ToolResult{})
	c.BeginTurn(2)
	if hint := c.Hint(); strings.Contains(hint, "without new") {
		t.Fatalf("idle hint should clear after progress: %q", hint)
	}
}

func TestEfficiencySearchSignatureStable(t *testing.T) {
	a := searchSignature(tool.GrepToolName, []byte(`{"pattern":"foo","path":"internal"}`))
	b := searchSignature(tool.GrepToolName, []byte(`{"path":"internal","pattern":"foo"}`))
	if a == "" || a != b {
		t.Fatalf("search signature unstable: %q vs %q", a, b)
	}
}
