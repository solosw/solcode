package unit_tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/tool"
)

func TestChangeDescriptionLimit(t *testing.T) {
	workDir := t.TempDir()
	write := tool.NewWriteTool()

	for _, test := range []struct {
		name string
		desc string
		want bool
	}{
		{name: "accepts thirty Chinese characters", desc: strings.Repeat("汉", 30), want: false},
		{name: "rejects thirty-one Chinese characters", desc: strings.Repeat("汉", 31), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			content, err := write.Invoke(context.Background(), &tool.UseContext{WorkDir: workDir}, json.RawMessage(`{"file_path":"notes.txt","content":"hello","desc":"`+test.desc+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			if content.IsError != test.want {
				t.Fatalf("Invoke() error = %v, want %v; output: %s", content.IsError, test.want, content.Text)
			}
			if test.want && !strings.Contains(content.Text, "30 Chinese characters") {
				t.Fatalf("validation error = %q, want 30-character limit", content.Text)
			}
		})
	}
}

func TestToolExecutorRejectsOverlengthDesc(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewWriteTool())

	result := engine.NewToolExecutor(registry, nil).Execute(context.Background(), engine.ToolCall{
		Name:  tool.WriteToolName,
		Input: json.RawMessage(`{"file_path":"notes.txt","content":"hello","desc":"` + strings.Repeat("汉", 31) + `"}`),
	}, engine.ToolEnv{UseContext: &tool.UseContext{WorkDir: t.TempDir()}})

	if !result.IsError || result.Content == nil {
		t.Fatalf("Execute() = %#v, want validation error", result)
	}
	if !strings.Contains(result.Content.Text, "30 Chinese characters") {
		t.Fatalf("validation error = %q, want 30-character limit", result.Content.Text)
	}
}

func TestMutationToolDescSchema(t *testing.T) {
	const want = "Optional description of this change (up to 30 Chinese characters)"
	for _, test := range []struct {
		name string
		tool tool.Tool
	}{
		{name: "Edit", tool: tool.NewEditTool()},
		{name: "Write", tool: tool.NewWriteTool()},
		{name: "Patch", tool: tool.NewPatchTool()},
	} {
		t.Run(test.name, func(t *testing.T) {
			properties := test.tool.InputSchema()["properties"].(map[string]any)
			desc := properties["desc"].(map[string]any)
			if got := desc["description"]; got != want {
				t.Errorf("desc description = %q, want %q", got, want)
			}
		})
	}
}
