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

func TestEfficiencyDifferentReadWindowsAreNotRepeats(t *testing.T) {
	c := NewRequestEfficiencyController()
	c.ObserveCall(tool.ViewToolName, []byte(`{"path":"a.go","offset":0,"limit":10}`), ToolResult{})
	c.ObserveCall(tool.ViewToolName, []byte(`{"path":"a.go","offset":10,"limit":10}`), ToolResult{})
	if hint := c.Hint(); hint != "" {
		t.Fatalf("pagination is new information: %s", hint)
	}
	if readSignature(tool.ViewToolName, viewInput("a.go")) != readSignature(tool.ViewToolName, []byte(`{"path":"a.go","offset":0,"limit":2000}`)) {
		t.Fatal("default read windows should normalize identically")
	}
}

func TestEfficiencyEditsAllowFreshReadsAndSearches(t *testing.T) {
	c := NewRequestEfficiencyController()
	for i := 0; i < 2; i++ {
		c.ObserveCall(tool.ViewToolName, viewInput("a.go"), ToolResult{})
		c.ObserveCall(tool.GrepToolName, []byte(`{"pattern":"foo"}`), ToolResult{})
	}
	if c.Hint() == "" {
		t.Fatal("expected repeats before mutation")
	}
	c.ObserveCall(tool.EditToolName, []byte(`{"path":"a.go"}`), ToolResult{})
	c.ObserveCall(tool.ViewToolName, viewInput("a.go"), ToolResult{})
	c.ObserveCall(tool.GrepToolName, []byte(`{"pattern":"foo"}`), ToolResult{})
	if hint := c.Hint(); hint != "" {
		t.Fatalf("new versions require fresh reads: %s", hint)
	}
}

func TestEfficiencyBatchMatchesGeneratedIDsAndErrors(t *testing.T) {
	c := NewRequestEfficiencyController()
	input := []byte(`{"operations":[{"tool":"View","input":{"path":"a.go"}},{"id":"op_1","tool":"View","input":{"path":"b.go"}},{"tool":"Grep","input":{"pattern":"foo"}}]}`)
	result := ToolResult{Content: tool.Result(`{"results":[{"id":"op_1_","status":"success"},{"id":"op_1","status":"error","error":{"message":"permission denied"}},{"id":"op_3","status":"success"}]}`)}
	c.ObserveCall(tool.BatchToolName, input, result)
	c.ObserveCall(tool.BatchToolName, input, result)
	hint := c.Hint()
	if !strings.Contains(hint, "a.go") || !strings.Contains(hint, "Repeated identical search") || !strings.Contains(hint, "permission denied") || strings.Contains(hint, "b.go") {
		t.Fatalf("batch signals not correlated correctly: %s", hint)
	}
}

func TestEfficiencySearchOptionsAffectSignature(t *testing.T) {
	if searchSignature(tool.GrepToolName, []byte(`{"pattern":"foo","case_sensitive":true}`)) == searchSignature(tool.GrepToolName, []byte(`{"pattern":"foo","case_sensitive":false}`)) {
		t.Fatal("different search options must not be treated as identical")
	}
}
