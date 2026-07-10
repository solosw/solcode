package unit_tests

import (
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestBashTool_IsDestructive(t *testing.T) {
	b := tool.NewBashTool()
	if !b.IsDestructive(nil) {
		t.Fatal("Bash should be destructive")
	}
	if b.IsReadOnly(nil) {
		t.Fatal("Bash should NOT be read-only")
	}
}

func TestTruncateOutput(t *testing.T) {
	long := strings.Repeat("x", 1000)
	short := tool.TruncateOutput(long, 100)
	if len(short) > 150 {
		t.Fatalf("expected truncated (<150), got len=%d", len(short))
	}
	if tool.TruncateOutput("hello", 100) != "hello" {
		t.Fatal("short content should be unchanged")
	}
}
