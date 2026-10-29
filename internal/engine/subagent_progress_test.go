package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/tool"
)

func TestSubagentProgressStreamsWithRunAndToolIdentity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "non-streaming"
		if stream {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			var events []tool.AgentProgressEvent
			registry := tool.NewRegistry()
			registry.Register(&backgroundTestTool{name: "View", run: func(context.Context) *tool.ContentBlock { return tool.Result("view result") }})
			client := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
				if req.OnTextDelta == nil || req.OnThinkingDelta == nil {
					t.Fatal("child stream callbacks not wired")
				}
				var message sdk.Message
				if index == 0 {
					return backgroundMessage(t, backgroundCall("view-1", "View", map[string]any{"path": "file"})), nil
				}
				raw := []byte(`{"id":"m","type":"message","role":"assistant","model":"test","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"thought","signature":"sig"},{"type":"text","text":"answer"}]}`)
				if err := json.Unmarshal(raw, &message); err != nil {
					t.Fatal(err)
				}
				if stream {
					req.OnThinkingDelta("thou")
					req.OnThinkingDelta("ght")
					req.OnTextDelta("ans")
					req.OnTextDelta("wer")
				}
				return &message, nil
			}}
			eng := NewEngine(Config{Client: client, Tools: registry, ModelName: "test", MaxTurns: 3, Stream: stream,
				OnTextDelta:     func(string) { t.Error("child text reached the main callback") },
				OnThinkingDelta: func(string) { t.Error("child thought reached the main callback") },
				OnAgentProgress: func(event tool.AgentProgressEvent) { events = append(events, event) },
			})
			result := eng.Run(t.Context(), agent.AgentConfig{ID: "child", RunID: "run", ParentID: "main", ParentToolUseID: "delegate", TaskID: "task", Description: "inspect", Role: agent.AgentRoleTask, WorkDir: t.TempDir(), Prompt: "inspect", MaxTurns: 3})
			if result.Error != "" {
				t.Fatal(result.Error)
			}
			text, thought := "", ""
			var toolKinds []string
			for _, event := range events {
				if event.AgentID != "child" || event.RunID != "run" || event.ParentAgentID != "main" || event.ParentToolUseID != "delegate" {
					t.Fatalf("event identity = %+v", event)
				}
				switch event.Kind {
				case "text_delta":
					text += event.Output
				case "thinking_delta":
					thought += event.Output
				case "tool_start", "tool_done":
					toolKinds = append(toolKinds, event.Kind)
					if event.ToolUseID != "view-1" || event.ToolName != "View" {
						t.Fatalf("tool identity = %+v", event)
					}
				}
			}
			if text != "answer" || thought != "thought" || !reflect.DeepEqual(toolKinds, []string{"tool_start", "tool_done"}) {
				t.Fatalf("text=%q thought=%q toolKinds=%v events=%+v", text, thought, toolKinds, events)
			}
		})
	}
}
