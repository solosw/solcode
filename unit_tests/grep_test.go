package unit_tests

import (
	"testing"

	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestGrepTool_ReadOnly(t *testing.T) {
	g := tool.NewGrepTool()
	if !g.IsReadOnly(nil) {
		t.Fatal("Grep should be read-only")
	}
}

func TestGlobToRegex(t *testing.T) {
	tests := []struct {
		glob     string
		expected string
	}{
		{"*.go", `.*\.go`},
		{"*.{ts,tsx}", `.*\.(ts|tsx)`},
	}
	for _, tt := range tests {
		got := tool.GlobToRegex(tt.glob)
		if got != tt.expected {
			t.Errorf("GlobToRegex(%s) = %s, want %s", tt.glob, got, tt.expected)
		}
	}
}
