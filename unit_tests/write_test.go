package unit_tests

import (
	"strings"
	"testing"

	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestWriteTool_IsDestructive(t *testing.T) {
	w := tool.NewWriteTool()
	if !w.IsDestructive(nil) {
		t.Fatal("Write should be destructive")
	}
}

func TestGenerateSimpleDiff_NewFile(t *testing.T) {
	diff := tool.GenerateSimpleDiff("", "hello\nworld\n", "test.txt")
	if !strings.Contains(diff, "+++ b/test.txt") {
		t.Fatal("diff should reference new file")
	}
	if !strings.Contains(diff, "+hello") {
		t.Fatal("diff should contain added lines")
	}
}

func TestGenerateSimpleDiff_Modified(t *testing.T) {
	old := "line1\nline2\nline3\n"
	new := "line1\nline2_modified\nline3\n"
	diff := tool.GenerateSimpleDiff(old, new, "test.txt")
	if !strings.Contains(diff, "- line2") || !strings.Contains(diff, "+ line2_modified") {
		t.Fatalf("diff should contain removed and added lines, got:\n%s", diff)
	}
}

func TestCountDiffChanges(t *testing.T) {
	diff := "--- a/test.go\n+++ b/test.go\n- old line\n+ new line\n+ extra line"
	add, rem := tool.CountDiffChanges(diff)
	if add != 2 || rem != 1 {
		t.Fatalf("expected +2 -1, got +%d -%d", add, rem)
	}
}
