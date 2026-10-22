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
		name                                                                 string
		mode                                                                 permission.Mode
		allowed                                                              []string
		permissionAllow, allowBash                                           []string
		denyWrite, denyBash, hookBlock, guardrail, wantWrite, wantOuterError bool
		code, want                                                           string
	}{
		{name: "allowed read and denied write", mode: permission.ModeBypass, allowed: []string{tool.ViewToolName}, code: `const a=await tools.View({path:"a.txt"}); const b=await tools.Write({path:"b.txt",content:"x"}); console.log(a.ok,b.ok,b.content);`, want: "true false tool"},
		{name: "container grants no children", mode: permission.ModeBypass, allowed: []string{tool.BatchToolName}, code: `console.log((await tools.View({path:"a.txt"})).content);`, want: "allowed_tools"},
		{name: "permission denial", mode: permission.ModeBypass, denyWrite: true, code: `console.log((await tools.Write({path:"b.txt",content:"x"})).content);`, want: "tool denied for session"},
		{name: "pre-tool hook", mode: permission.ModeBypass, hookBlock: true, code: `console.log((await tools.Write({path:"b.txt",content:"x"})).content);`, want: "hook blocked child"},
		{name: "guardrail", mode: permission.ModeBypass, guardrail: true, code: `console.log((await tools.Write({path:"b.txt",content:"x"})).content);`, want: "guardrail blocked child"},
		{name: "checkpoint preserved after failure", mode: permission.ModeBypass, wantWrite: true, wantOuterError: true, code: `await tools.Write({path:"b.txt",content:"x"}); throw new Error("after write");`, want: "after write"},
		{name: "plan allows read-only code", mode: permission.ModePlan, code: `const r=await tools.View({path:"a.txt"}); console.log(r.ok,r.content);`, want: "true <file>"},
		{name: "plan denies write child", mode: permission.ModePlan, code: `const r=await tools.Write({path:"b.txt",content:"x"}); console.log(r.ok,r.content);`, want: "false plan mode only allows read-only"},
		{name: "plan mixed batch keeps read access", mode: permission.ModePlan, code: `const r=await Promise.all([tools.View({path:"a.txt"}),tools.Write({path:"b.txt",content:"x"})]); console.log(r[0].ok,r[1].ok,r[1].content);`, want: "true false plan mode only allows read-only"},
		{name: "plan write allowlist cannot override child permissions", mode: permission.ModePlan, permissionAllow: []string{tool.BatchToolName, tool.WriteToolName}, code: `const r=await tools.Write({path:"b.txt",content:"x"}); console.log(r.ok,r.content);`, want: "false plan mode only allows read-only"},
		{name: "plan allows bash child", mode: permission.ModePlan, code: `const r=await tools.Bash({command:"echo plan-bash"}); console.log(r.ok,r.content);`, want: "true plan-bash"},
		{name: "plan allows bash with configured allowlist", mode: permission.ModePlan, allowBash: []string{"echo"}, code: `const r=await tools.Bash({command:"echo plan-bash"}); console.log(r.ok,r.content);`, want: "true plan-bash"},
		{name: "plan bash respects session denial", mode: permission.ModePlan, denyBash: true, code: `const r=await tools.Bash({command:"echo plan-bash"}); console.log(r.ok,r.content);`, want: "false tool denied for session"},
		{name: "plan bash respects agent allowlist", mode: permission.ModePlan, allowed: []string{tool.ViewToolName}, code: `const r=await tools.Bash({command:"echo plan-bash"}); console.log(r.ok,r.content);`, want: "allowed_tools"},
		{name: "plan respects agent child allowlist", mode: permission.ModePlan, allowed: []string{tool.BatchToolName}, code: `const r=await tools.View({path:"a.txt"}); console.log(r.ok,r.content);`, want: "allowed_tools"},
		{name: "plan respects explicit child denial", mode: permission.ModePlan, denyWrite: true, code: `const r=await tools.Write({path:"b.txt",content:"x"}); console.log(r.ok,r.content);`, want: "tool denied for session"},
		{name: "plan read-only agent cannot write", mode: permission.ModePlan, allowed: []string{tool.ViewToolName}, code: `const a=await tools.View({path:"a.txt"}); const b=await tools.Write({path:"b.txt",content:"x"}); console.log(a.ok,b.ok,b.content);`, want: "true false plan mode only allows read-only"},
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
			registry.Register(tool.NewBatchToolWithPTC(tool.DefaultBatchLimits, true), tool.NewViewTool(), tool.NewWriteTool(), tool.NewBashTool())
			permissions := permission.NewServiceWithConfig(permission.Config{Mode: tc.mode, Allow: tc.permissionAllow, AllowBash: tc.allowBash})
			if tc.denyWrite {
				permissions.DenyTool(tool.WriteToolName)
			}
			if tc.denyBash {
				permissions.DenyTool(tool.BashToolName)
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
