package engine

import (
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestSearchContextReplacesLegacyReads(t *testing.T) {
	all := []tool.Tool{tool.NewSearchContextTool(nil, nil), tool.NewReadMemoryTool(nil), tool.NewReadSessionMemoryTool(nil)}
	enabled := map[string]bool{tool.ReadMemoryToolName: true, tool.ReadSessionMemoryToolName: true}
	for _, allowed := range [][]string{nil, {tool.ReadMemoryToolName, tool.ReadSessionMemoryToolName}} {
		selected := namesOf(SelectToolsForTurn(all, allowed, "ReadMemory ReadSessionMemory search memory", enabled))
		if !selected[tool.SearchContextToolName] || selected[tool.ReadMemoryToolName] || selected[tool.ReadSessionMemoryToolName] {
			t.Fatalf("model tool selection = %+v", selected)
		}
	}
}
