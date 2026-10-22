package permission

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestPlanModeOverridesToolAllowlists(t *testing.T) {
	for _, source := range []string{"config", "session"} {
		for _, tc := range []struct {
			tool  tool.Tool
			input json.RawMessage
		}{
			{tool.NewWriteTool(), json.RawMessage(`{"path":"a.txt","content":"x"}`)},
			{tool.NewEditTool(), json.RawMessage(`{"path":"a.txt","old_string":"a","new_string":"b"}`)},
			{tool.NewAskUserTool(), json.RawMessage(`{}`)},
			{tool.NewBatchTool(), json.RawMessage(`{"operations":[{"tool":"Write","input":{"path":"a.txt","content":"x"}}]}`)},
		} {
			t.Run(source+"/"+tc.tool.Name(), func(t *testing.T) {
				cfg := Config{Mode: ModeAuto}
				if source == "config" {
					cfg.Allow = []string{tc.tool.Name()}
				}
				svc := NewServiceWithConfig(cfg)
				if source == "session" {
					svc.AllowTool(tc.tool.Name())
				}
				if d := svc.Check(tc.tool, tc.input); !d.Allowed {
					t.Fatalf("allowlist should apply before plan: %+v", d)
				}
				svc.SetMode(ModePlan)
				if d := svc.Check(tc.tool, tc.input); d.Allowed || !strings.Contains(d.Reason, "plan mode") {
					t.Fatalf("allowlist bypassed plan: %+v", d)
				}
				svc.SetMode(ModeAuto)
				if d := svc.Check(tc.tool, tc.input); !d.Allowed {
					t.Fatalf("leaving plan should preserve allowlist: %+v", d)
				}
			})
		}
	}
}

func TestPlanModeAllowsBashAsExplicitException(t *testing.T) {
	svc := NewService(ModePlan)
	asked := false
	svc.SetAskFunc(func(string, string) bool {
		asked = true
		return false
	})
	bash := tool.NewBashTool()
	input := json.RawMessage(`{"command":"echo plan-bash"}`)
	if bash.IsReadOnly(input) {
		t.Fatal("Bash must not be classified as read-only")
	}
	if d := svc.Check(bash, input); !d.Allowed || asked {
		t.Fatalf("plan should permit Bash without an allowlist or approval: %+v, asked=%v", d, asked)
	}
	svc.DenyTool(tool.BashToolName)
	if d := svc.Check(bash, input); d.Allowed || !strings.Contains(d.Reason, "tool denied for session") {
		t.Fatalf("Bash exception must respect explicit session denial: %+v", d)
	}
}

func TestPlanModeAllowsPTCContainerWithoutReadOnlyClassification(t *testing.T) {
	ptc := tool.NewBatchToolWithPTC(tool.DefaultBatchLimits, true)
	input := json.RawMessage(`{"code":"console.log(await tools.View({path:'a.txt'}));"}`)
	if ptc.IsReadOnly(input) {
		t.Fatal("arbitrary JavaScript must not be classified as read-only")
	}
	svc := NewService(ModePlan)
	if d := svc.Check(ptc, input); !d.Allowed {
		t.Fatalf("PTC container should dispatch individually checked children: %+v", d)
	}
	for _, allowed := range []tool.Tool{ptc, tool.NewViewTool(), tool.NewBashTool(), tool.NewTodoWriteTool(), tool.NewModeSwitchTool(nil)} {
		svc.DenyTool(allowed.Name())
		if d := svc.Check(allowed, input); d.Allowed || !strings.Contains(d.Reason, "tool denied for session") {
			t.Fatalf("plan exception bypassed session denial for %s: %+v", allowed.Name(), d)
		}
	}
}

func TestPlanModeShortInstructionsAreConditional(t *testing.T) {
	for _, text := range []string{
		"only while the current permission mode is plan",
		"latest mode context",
		"When plan mode is inactive, these rules do not restrict implementation or tool use",
	} {
		if !strings.Contains(PlanModeShortInstructions, text) {
			t.Fatalf("stable plan instructions missing condition %q", text)
		}
	}
}
