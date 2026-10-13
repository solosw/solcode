package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestBatchJSONEnvelopeNotRecompressed(t *testing.T) {
	var code strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&code, "%04d|func thing%d() { return %q }\n", i, i, strings.Repeat("source", 10))
	}
	payload, _ := json.Marshal(map[string]any{"batch_id": "batch_test", "status": "partial_success", "summary": map[string]int{"succeeded": 1, "failed": 1}, "results": []any{
		map[string]any{"id": "a", "tool": "View", "status": "success", "output": map[string]any{"content": code.String()}},
		map[string]any{"id": "b", "tool": "View", "status": "error", "error": map[string]any{"code": "FILE_NOT_FOUND", "message": "file missing"}},
	}})
	result, err := NewRuntime(DefaultConfig()).Run(context.Background(), Event{Name: EventPostToolUse, ToolName: tool.BatchToolName, ToolResult: tool.Result(string(payload))})
	if err != nil {
		t.Fatal(err)
	}
	if result.ModifiedResult != nil || result.Decision != DecisionAllow {
		t.Fatalf("batch envelope must not be recompressed: %+v", result)
	}
}

func TestBatchManagedChildSkipsOnlyBuiltinCompression(t *testing.T) {
	text := strings.Repeat("INFO worker finished task id="+strings.Repeat("a", 32)+" payload="+strings.Repeat("b", 64)+"\n", 400)
	result, err := NewRuntime(DefaultConfig()).Run(context.Background(), Event{Name: EventPostToolUse, ToolName: tool.BashToolName, ToolResult: tool.Result(text), OutputManaged: true})
	if err != nil || result.ModifiedResult != nil || result.Decision != DecisionAllow {
		t.Fatalf("managed child must stay lossless: %+v, %v", result, err)
	}
}
