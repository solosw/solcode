package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/hook"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

type ptcTestGuardrail struct{}

func (ptcTestGuardrail) CheckToolCall(_ context.Context, name string, _ json.RawMessage) string {
	if name == tool.WriteToolName {
		return "guardrail blocked child"
	}
	return ""
}

func TestPTCChildExecutorSafety(t *testing.T) {
	for _, tc := range []struct {
		name                                                       string
		mode                                                       permission.Mode
		allowed                                                    []string
		denyWrite, hookBlock, guardrail, wantWrite, wantOuterError bool
		code, want                                                 string
	}{
		{name: "allowed read and denied write", mode: permission.ModeBypass, allowed: []string{tool.ViewToolName}, code: `const a=await tools.View({path:"a.txt"}); const b=await tools.Write({path:"b.txt",content:"x"}); console.log(a.ok,b.ok,b.content);`, want: "true false tool"},
		{name: "container grants no children", mode: permission.ModeBypass, allowed: []string{tool.BatchToolName}, code: `console.log((await tools.View({path:"a.txt"})).content);`, want: "allowed_tools"},
		{name: "permission denial", mode: permission.ModeBypass, denyWrite: true, code: `console.log((await tools.Write({path:"b.txt",content:"x"})).content);`, want: "tool denied for session"},
		{name: "pre-tool hook", mode: permission.ModeBypass, hookBlock: true, code: `console.log((await tools.Write({path:"b.txt",content:"x"})).content);`, want: "hook blocked child"},
		{name: "guardrail", mode: permission.ModeBypass, guardrail: true, code: `console.log((await tools.Write({path:"b.txt",content:"x"})).content);`, want: "guardrail blocked child"},
		{name: "checkpoint preserved after failure", mode: permission.ModeBypass, wantWrite: true, wantOuterError: true, code: `await tools.Write({path:"b.txt",content:"x"}); throw new Error("after write");`, want: "after write"},
		{name: "plan conservatively denies arbitrary code", mode: permission.ModePlan, wantOuterError: true, code: `await tools.Write({path:"b.txt",content:"x"});`, want: "plan mode only allows read-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hookBlock && runtime.GOOS == "windows" {
				t.Skip("POSIX hook fixture")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("read me"), 0600); err != nil {
				t.Fatal(err)
			}
			registry := tool.NewRegistry()
			registry.Register(tool.NewBatchToolWithPTC(tool.DefaultBatchLimits, true), tool.NewViewTool(), tool.NewWriteTool())
			permissions := permission.NewService(tc.mode)
			if tc.denyWrite {
				permissions.DenyTool(tool.WriteToolName)
			}
			var hooks *hook.Runtime
			if tc.hookBlock {
				hooks = hook.NewRuntime(hook.Config{Events: map[hook.EventName][]hook.MatcherConfig{
					hook.EventPreToolUse: {{Matcher: tool.WriteToolName, Hooks: []hook.CommandConfig{{Type: "command", Command: `printf '%s' '{"decision":"block","message":"hook blocked child"}'`}}}},
				}})
			}
			executor := NewToolExecutorWithPermissions(registry, hooks, permissions).WithAllowedTools(tc.allowed)
			if tc.guardrail {
				executor.WithGuardrail(ptcTestGuardrail{})
			}
			captured := map[string]*string{}
			uctx := &tool.UseContext{WorkDir: dir, BatchLookup: registry.Find, CaptureCheckpoint: func(path string, content *string) { captured[path] = content }}
			uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*tool.ContentBlock, error) {
				return executor.Execute(ctx, ToolCall{Name: name, Input: input}, ToolEnv{UseContext: uctx, OutputManaged: true}).Content, nil
			}
			input, _ := json.Marshal(map[string]string{"code": tc.code})
			out := executor.Execute(t.Context(), ToolCall{Name: tool.BatchToolName, Input: input}, ToolEnv{UseContext: uctx})
			if out.IsError != tc.wantOuterError || out.Content == nil || !strings.Contains(out.Content.Text, tc.want) {
				t.Fatalf("unexpected result: %+v / %+v", out, out.Content)
			}
			path := filepath.Join(dir, "b.txt")
			data, err := os.ReadFile(path)
			if tc.wantWrite {
				if err != nil || string(data) != "x" {
					t.Fatalf("write lost: %q / %v", data, err)
				}
				if content, ok := captured[path]; !ok || content != nil {
					t.Fatalf("creation checkpoint missing: %v", captured)
				}
			} else if !os.IsNotExist(err) || len(captured) != 0 {
				t.Fatalf("denied call mutated file/checkpoint: %v / %v", err, captured)
			}
		})
	}
}

func TestPTCEngineResultStoreKeepsFullChildOutput(t *testing.T) {
	full := strings.Repeat("INFO worker finished task id=abcdefghijklmnopqrstuvwxyz payload=0123456789\n", 400)
	registry := tool.NewRegistry()
	limits := tool.DefaultBatchLimits
	limits.MaxResultRunes = 100
	registry.Register(tool.NewBatchToolWithPTC(limits, true), &backgroundTestTool{name: tool.BashToolName, run: func(context.Context) *tool.ContentBlock { return tool.Result(full) }})
	calls := 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if calls == 1 {
			return backgroundMessage(t, backgroundCall("ptc", tool.BatchToolName, map[string]any{"code": `
			const r=await tools.Bash({}); if (!r.ok) throw new Error(r.error || r.content); let n=r.content.length;
			for(let offset=r.next_offset;offset<r.total_runes;) {
			  const page=results.get(r.result_ref,{offset,limit:100}); n+=page.content.length;offset=page.next_offset;
			} console.log(r.truncated,n,r.total_runes);`})), nil
		}
		texts := backgroundResultTexts(req.Messages, "ptc")
		if len(texts) != 1 || texts[0] != fmt.Sprintf("true %d %d", len(full), len(full)) {
			t.Fatalf("full child output not retained: %v (expected length %d)", texts, len(full))
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, Hooks: hook.NewRuntime(hook.DefaultConfig()), MaxTurns: 3})
	result := eng.RunWithHistory(t.Context(), RunRequest{AgentConfig: agent.AgentConfig{ID: "main", Prompt: "run PTC", WorkDir: t.TempDir()}})
	if result.AgentResult.Error != "" || calls != 2 {
		t.Fatalf("engine failed: %s calls=%d", result.AgentResult.Error, calls)
	}
}
