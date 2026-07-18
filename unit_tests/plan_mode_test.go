package unit_tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

type planModeTool struct {
	name     string
	readOnly bool
}

func (t *planModeTool) Name() string                       { return t.name }
func (t *planModeTool) Description() string                { return t.name }
func (t *planModeTool) InputSchema() map[string]any        { return map[string]any{} }
func (t *planModeTool) IsDestructive(json.RawMessage) bool { return !t.readOnly }
func (t *planModeTool) IsReadOnly(json.RawMessage) bool    { return t.readOnly }
func (t *planModeTool) IsConcurrencySafe(json.RawMessage) bool {
	return true
}
func (t *planModeTool) Aliases() []string { return nil }
func (t *planModeTool) ValidateInput(context.Context, json.RawMessage) error {
	return nil
}
func (t *planModeTool) Invoke(context.Context, *tool.UseContext, json.RawMessage) (*tool.ContentBlock, error) {
	return tool.Result("ok"), nil
}

func TestPlanModeAllowsReadOnlyTodoWriteAndTask(t *testing.T) {
	svc := permission.NewService(permission.ModePlan)
	input := json.RawMessage(`{}`)

	cases := []struct {
		name    string
		tool    tool.Tool
		wantOK  bool
		wantSub string
	}{
		{"View", &planModeTool{name: "View", readOnly: true}, true, ""},
		{"TodoWrite", &planModeTool{name: tool.TodoWriteToolName, readOnly: false}, true, ""},
		{"Task", &planModeTool{name: tool.TaskToolName, readOnly: false}, true, ""},
		{"Edit", &planModeTool{name: "Edit", readOnly: false}, false, "plan mode"},
		{"Write", &planModeTool{name: "Write", readOnly: false}, false, "plan mode"},
		{"Bash", &planModeTool{name: "Bash", readOnly: false}, false, "plan mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := svc.Check(tc.tool, input)
			if d.Allowed != tc.wantOK {
				t.Fatalf("allowed=%v want %v reason=%q", d.Allowed, tc.wantOK, d.Reason)
			}
			if !tc.wantOK && tc.wantSub != "" && !strings.Contains(d.Reason, tc.wantSub) {
				t.Fatalf("reason %q missing %q", d.Reason, tc.wantSub)
			}
		})
	}
}

func TestWrapPlanModePromptIdempotent(t *testing.T) {
	once := permission.WrapPlanModePrompt("design auth flow")
	if !strings.Contains(once, permission.PlanModePromptMarker) {
		t.Fatal("expected plan mode marker")
	}
	if !strings.Contains(once, "design auth flow") {
		t.Fatal("expected original user prompt")
	}
	if !strings.Contains(once, "sub-agents") {
		t.Fatal("expected sub-agent guidance")
	}
	for _, section := range []string{
		"## Goal",
		"## Current State",
		"## Approach",
		"## Architecture & Trade-offs",
		"## Implementation Plan",
		"## Test & Validation Plan",
		"## Open Questions",
		"## Critical Files for Implementation",
		"Required Output Format",
	} {
		if !strings.Contains(once, section) {
			t.Fatalf("plan instructions missing section %q", section)
		}
	}
	twice := permission.WrapPlanModePrompt(once)
	if strings.Count(twice, permission.PlanModePromptMarker) != 1 {
		t.Fatalf("expected single marker, got %d", strings.Count(twice, permission.PlanModePromptMarker))
	}
}

func TestStripPlanModePrompt(t *testing.T) {
	wrapped := permission.WrapPlanModePrompt("design auth flow")
	stripped := permission.StripPlanModePrompt(wrapped)
	if strings.Contains(stripped, permission.PlanModePromptMarker) {
		t.Fatalf("marker should be removed, got %q", stripped)
	}
	if stripped != "design auth flow" {
		t.Fatalf("expected original user text, got %q", stripped)
	}
	if got := permission.StripPlanModePrompt("plain"); got != "plain" {
		t.Fatalf("unexpected strip: %q", got)
	}
}

func TestAppendPlanModeSystemPrompt(t *testing.T) {
	base := "You are solcode."
	once := permission.AppendPlanModeSystemPrompt(base)
	if !strings.HasPrefix(once, base) {
		t.Fatalf("expected system prompt prefix, got %q", once)
	}
	if !strings.Contains(once, permission.PlanModePromptMarker) {
		t.Fatal("expected plan mode marker appended to system prompt")
	}
	if !strings.Contains(once, permission.PlanModeInstructions) {
		t.Fatal("expected full plan instructions in system prompt")
	}
	twice := permission.AppendPlanModeSystemPrompt(once)
	if strings.Count(twice, permission.PlanModePromptMarker) != 1 {
		t.Fatalf("expected single marker after re-append, got %d", strings.Count(twice, permission.PlanModePromptMarker))
	}
}

func TestContextBuilderPlanModeSystemPrompt(t *testing.T) {
	on := engine.ContextBuilder{SystemPrompt: "custom", PlanMode: true}
	reqOn := on.Build(engine.BuildRequest{WorkDir: "/tmp"})
	if !strings.Contains(reqOn.System, permission.PlanModePromptMarker) {
		t.Fatal("plan mode should append plan instructions to system prompt")
	}
	if !strings.Contains(reqOn.System, "custom") {
		t.Fatal("custom system prompt should still be present")
	}

	off := engine.ContextBuilder{SystemPrompt: "custom", PlanMode: false}
	reqOff := off.Build(engine.BuildRequest{WorkDir: "/tmp"})
	if strings.Contains(reqOff.System, permission.PlanModePromptMarker) {
		t.Fatal("non-plan mode must strip plan instructions from system prompt")
	}

	stale := engine.ContextBuilder{
		SystemPrompt: permission.AppendPlanModeSystemPrompt("custom"),
		PlanMode:     false,
	}
	reqStale := stale.Build(engine.BuildRequest{WorkDir: "/tmp"})
	if strings.Contains(reqStale.System, permission.PlanModePromptMarker) {
		t.Fatal("stale plan block must be stripped when not in plan mode")
	}
	if !strings.Contains(reqStale.System, "custom") {
		t.Fatal("custom text should remain after stripping plan block")
	}

	staleOn := engine.ContextBuilder{
		SystemPrompt: permission.AppendPlanModeSystemPrompt("custom"),
		PlanMode:     true,
	}
	reqStaleOn := staleOn.Build(engine.BuildRequest{WorkDir: "/tmp"})
	if strings.Count(reqStaleOn.System, permission.PlanModePromptMarker) != 1 {
		t.Fatalf("expected single plan marker, got %d", strings.Count(reqStaleOn.System, permission.PlanModePromptMarker))
	}
}
