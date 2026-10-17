package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeNativeDesktop struct {
	calls  int
	action string
	input  json.RawMessage
}

func (f *fakeNativeDesktop) ListTools(context.Context) ([]MCPToolInfo, error) {
	return []MCPToolInfo{{ToolName: "click", Description: "Click target", InputSchema: map[string]any{"type": "object", "required": []string{"app"}}}}, nil
}
func (f *fakeNativeDesktop) CallTool(_ context.Context, action string, input json.RawMessage) (*ContentBlock, error) {
	f.calls++
	f.action = action
	f.input = append([]byte(nil), input...)
	return ImageResult("image/png", "YWJj", "app screenshot"), nil
}
func (*fakeNativeDesktop) Close() error { return nil }

func TestNativeComputerUseSafety(t *testing.T) {
	tool := NewNativeComputerUseTool(nil)
	for _, tc := range []struct {
		input string
		read  bool
	}{
		{`{"action":"describe"}`, true}, {`{"action":"list_apps"}`, true},
		{`{"action":"get_app_state","arguments":{"app":"Editor"}}`, false},
		{`{"action":"get_app_state","arguments":{"app":"Editor","window_placement":"keep"}}`, false},
		{`{"action":"get_app_state","arguments":{"app":"Editor","window_placement":"restore"}}`, false},
		{`{"action":"get_app_state","arguments":{"app":"Editor","window_placement":"agent_display"}}`, false},
		{`{"action":"query","arguments":{"app":"Editor"}}`, true},
		{`{"action":"click","arguments":{"app":"Editor","element_index":"0"}}`, false},
		{`{"action":"screenshot"}`, false}, {`{"action":"list_apps","arguments":null}`, false},
		{`{"action":"get_app_state","arguments":[]}`, false}, {`{"action":"list_apps","x":1}`, false},
		{`{"action":"list_apps"} {}`, false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			in := json.RawMessage(tc.input)
			if tool.IsReadOnly(in) != tc.read || tool.IsDestructive(in) == tc.read {
				t.Fatal("unsafe permission classification")
			}
			if tool.IsConcurrencySafe(in) {
				t.Fatal("desktop calls must serialize")
			}
		})
	}
}
func TestNativeComputerUsePassesArgumentsAndImage(t *testing.T) {
	backend := &fakeNativeDesktop{}
	tool := NewNativeComputerUseTool(backend)
	result, err := tool.Invoke(context.Background(), nil, json.RawMessage(`{"action":"get_app_state","arguments":{"app":"Editor"}}`))
	if err != nil || result.IsError || result.Type != "image" || result.Data != "YWJj" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if backend.action != "get_app_state" || string(backend.input) != `{"app":"Editor"}` {
		t.Fatalf("incorrect native call: %+v", backend)
	}
}
func TestNativeComputerUseRejectsLegacyAndInvalidCalls(t *testing.T) {
	backend := &fakeNativeDesktop{}
	tool := NewNativeComputerUseTool(backend)
	for _, in := range []string{`{"action":"screenshot"}`, `{"action":"click","x":1,"y":2}`, `{"action":"type","text":"hello"}`, `{"action":"list_apps","arguments":null}`, `{"action":"list_apps"} {}`} {
		result, err := tool.Invoke(context.Background(), nil, json.RawMessage(in))
		if err != nil || !result.IsError {
			t.Fatalf("invalid call accepted: %s, %+v, %v", in, result, err)
		}
	}
	if backend.calls != 0 {
		t.Fatal("invalid calls reached desktop")
	}
}
func TestNativeComputerUseDescribeIsTargeted(t *testing.T) {
	tool := NewNativeComputerUseTool(&fakeNativeDesktop{})
	result, err := tool.Invoke(context.Background(), nil, json.RawMessage(`{"action":"describe","arguments":{"tool":"click"}}`))
	if err != nil || result.IsError || !strings.Contains(result.Text, `"required":["app"]`) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, _ = tool.Invoke(context.Background(), nil, json.RawMessage(`{"action":"describe","arguments":{"tool":"query"}}`))
	if !result.IsError {
		t.Fatal("unavailable tool must not be advertised as supported")
	}
}
