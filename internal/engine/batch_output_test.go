package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestBatchArchivesChildOutputBeforeBuiltinLossyCompression(t *testing.T) {
	full := strings.Repeat("INFO worker finished task id="+strings.Repeat("a", 32)+" payload="+strings.Repeat("b", 64)+"\n", 400)
	registry := tool.NewRegistry()
	registry.Register(tool.NewBatchTool(), &backgroundTestTool{name: tool.BashToolName, run: func(context.Context) *tool.ContentBlock { return tool.Result(full) }})
	calls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if calls == 1 {
			return backgroundMessage(t, backgroundCall("batch", tool.BatchToolName, map[string]any{"operations": []any{map[string]any{"tool": tool.BashToolName, "input": map[string]any{}}}})), nil
		}
		texts := backgroundResultTexts(req.Messages, "batch")
		if len(texts) != 1 {
			t.Fatalf("batch results=%#v", texts)
		}
		var resp struct {
			Results []struct {
				Output struct {
					Content        string `json:"content"`
					FullOutputPath string `json:"full_output_path"`
				} `json:"output"`
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(texts[0]), &resp); err != nil {
			t.Fatalf("batch envelope corrupted: %v", err)
		}
		if len(resp.Results) != 1 || resp.Results[0].Output.FullOutputPath == "" {
			t.Fatalf("archive missing: %s", texts[0])
		}
		path := resp.Results[0].Output.FullOutputPath
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
		data, err := os.ReadFile(path)
		if err != nil || string(data) != full {
			t.Fatalf("builtin compressed child before archiving: archived bytes=%d, expected=%d, error=%v", len(data), len(full), err)
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, Hooks: hook.NewRuntime(hook.DefaultConfig()), MaxTurns: 3})
	result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Prompt: "batch work", WorkDir: t.TempDir()}})
	if result.AgentResult.Error != "" || calls != 2 {
		t.Fatalf("result=%+v requests=%d", result.AgentResult, calls)
	}
}

func TestBatchGeneratedIDsMatchWorkingSetStatuses(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "b.go")
	_ = os.WriteFile(a, []byte("package a\n"), 0600)
	_ = os.WriteFile(b, []byte("package b\n"), 0600)
	ws := NewWorkingSet()
	input := []byte(`{"operations":[{"tool":"View","input":{"path":"a.go"}},{"id":"op_1","tool":"View","input":{"path":"b.go"}}]}`)
	result := ToolResult{Content: tool.Result(`{"results":[{"id":"op_1_","status":"error"},{"id":"op_1","status":"success"}]}`)}
	observeBatchWorkingSet(ws, input, &tool.UseContext{WorkDir: dir}, result)
	if _, ok := ws.Lookup(a); ok {
		t.Fatal("failed read incorrectly cached under generated ID")
	}
	if got, ok := ws.Lookup(b); !ok || !strings.Contains(got, "package b") {
		t.Fatalf("successful read not cached: %q %v", got, ok)
	}
}

func TestBatchWrappedAllowlistAndPlanModePermissions(t *testing.T) {
	cases := []struct {
		name          string
		mode          permission.Mode
		allowed       []string
		body          string
		wantStatus    string
		wantSucceeded int
	}{
		{"read-only plan", permission.ModePlan, nil, `{"operations":[{"tool":"View","input":{"path":"a.txt"}}]}`, "success", 1},
		{"wrapped readonly allowlist", permission.ModeBypass, []string{tool.ViewToolName}, `{"operations":[{"tool":"View","input":{"path":"a.txt"}},{"tool":"Write","input":{"path":"b.txt","content":"forbidden"}}]}`, "partial_success", 1},
		{"container alone grants no children", permission.ModeBypass, []string{tool.BatchToolName}, `{"operations":[{"tool":"View","input":{"path":"a.txt"}}]}`, "failed", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("read me"), 0600)
			registry := tool.NewRegistry()
			registry.Register(tool.NewBatchTool(), tool.NewViewTool(), tool.NewWriteTool())
			executor := NewToolExecutorWithPermissions(registry, nil, permission.NewService(tc.mode)).WithAllowedTools(tc.allowed)
			uctx := &tool.UseContext{WorkDir: dir, BatchLookup: registry.Find}
			uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*tool.ContentBlock, error) {
				return executor.Execute(ctx, ToolCall{Name: name, Input: input}, ToolEnv{UseContext: uctx}).Content, nil
			}
			out := executor.Execute(t.Context(), ToolCall{Name: tool.BatchToolName, Input: json.RawMessage(tc.body)}, ToolEnv{UseContext: uctx})
			if out.IsError {
				t.Fatalf("batch container incorrectly denied: %s", out.Content.Text)
			}
			var resp struct {
				Status  string `json:"status"`
				Summary struct {
					Succeeded int `json:"succeeded"`
				} `json:"summary"`
			}
			if err := json.Unmarshal([]byte(out.Content.Text), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Status != tc.wantStatus || resp.Summary.Succeeded != tc.wantSucceeded {
				t.Fatalf("response=%s", out.Content.Text)
			}
			if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
				t.Fatal("batch bypassed child allowlist")
			}
		})
	}
}
