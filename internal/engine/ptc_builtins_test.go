package engine

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
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

func TestPTCPublicBuiltinsAndImageResult(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "pixel.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.NewBatchToolWithPTC(tool.DefaultBatchLimits, true), tool.NewViewTool(), tool.NewSolAPITool(), &backgroundTestTool{name: tool.DiffToolName, run: func(context.Context) *tool.ContentBlock { return tool.Result("extra builtin ran") }})
	calls, controls := 0, 0
	client := backgroundTestClient(func(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		calls++
		if calls == 1 {
			return backgroundMessage(t, backgroundCall("program", tool.BatchToolName, map[string]any{"code": `const d=await tools.Diff({}); if(!d.ok) throw Error(d.error||d.content); const h=await tools.solapi({action:"history"}); if(!h.ok) throw Error(h.error||h.content); const a=await tools.View({path:"pixel.png"}); const b=await tools.View({path:"pixel.png"}); if(!a.ok||!b.ok||!a.image_attached||!b.image_attached) throw Error(a.content||a.error); console.log(d.content,h.content,a.content);`})), nil
		}
		images := 0
		for _, message := range req.Messages {
			for _, block := range message.Content {
				if block.OfToolResult == nil || block.OfToolResult.ToolUseID != "program" {
					continue
				}
				for _, item := range block.OfToolResult.Content {
					if item.OfImage != nil {
						images++
					}
					if item.OfText != nil && strings.Contains(item.OfText.Text, "iVBOR") {
						t.Fatal("image bytes leaked into script output")
					}
				}
			}
		}
		if images != 2 {
			t.Fatalf("model received %d images, want 2; text=%v", images, backgroundResultTexts(req.Messages, "program"))
		}
		text := strings.Join(backgroundResultTexts(req.Messages, "program"), " ")
		if !strings.Contains(text, "extra builtin ran") || !strings.Contains(text, "current-history") {
			t.Fatalf("missing child results: %s", text)
		}
		return backgroundMessage(t), nil
	})
	eng := NewEngine(Config{Client: client, Tools: registry, Hooks: hook.NewRuntime(hook.DefaultConfig()), Permissions: permission.NewService(permission.ModeAuto), MaxTurns: 3, SolAPI: func(_ context.Context, id, workDir string, req tool.SolAPIRequest) (any, error) {
		controls++
		if id != "bound-session" || workDir != dir || req.Action != "history" {
			t.Fatalf("wrong session scope: %q %q %+v", id, workDir, req)
		}
		return map[string]string{"result": "current-history"}, nil
	}})
	result := eng.RunWithHistory(t.Context(), RunRequest{SessionID: "bound-session", AgentConfig: agent.AgentConfig{ID: "main", Role: agent.AgentRoleMain, WorkDir: dir, Prompt: "Inspect two images"}})
	if result.AgentResult.Error != "" || controls != 1 || calls != 2 {
		t.Fatalf("run failed: %+v controls=%d calls=%d", result.AgentResult, controls, calls)
	}
}

func TestPTCExtraBuiltinRespectsRuntimeAllowlist(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBatchToolWithPTC(tool.DefaultBatchLimits, true), tool.NewSolAPITool())
	executor := NewToolExecutorWithPermissions(registry, nil, permission.NewService(permission.ModeBypass)).WithAllowedTools([]string{tool.BatchToolName})
	uctx := &tool.UseContext{SessionID: "current", AgentRole: "main", BatchTools: registry.All, BatchLookup: registry.Find, SolAPI: func(context.Context, tool.SolAPIRequest) (any, error) { t.Fatal("denied child ran"); return nil, nil }}
	uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*tool.ContentBlock, error) {
		return executor.Execute(ctx, ToolCall{Name: name, Input: input}, ToolEnv{UseContext: uctx}).Content, nil
	}
	input, _ := json.Marshal(map[string]string{"code": `console.log((await tools.solapi({action:"history"})).content)`})
	out := executor.Execute(t.Context(), ToolCall{Name: tool.BatchToolName, Input: input}, ToolEnv{UseContext: uctx})
	if out.IsError || out.Content == nil || !strings.Contains(out.Content.Text, "allowed_tools") {
		t.Fatalf("allowlist not enforced: %+v", out.Content)
	}
}
