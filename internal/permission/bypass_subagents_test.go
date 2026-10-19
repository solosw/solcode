package permission

import (
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestBypassSubagentsDefaultAndRuntimeToggle(t *testing.T) {
	svc := NewService(ModeBypass)
	if svc.DelegationEnabled() {
		t.Fatal("bypass sub-agents must default to disabled")
	}
	delegate := tool.NewTaskTool(nil)
	input := []byte(`{"action":"spawn","agent_type":"worker"}`)
	svc.AllowTool(delegate.Name())
	svc.DenyTool(tool.WriteToolName)
	svc.SetAskFunc(func(string, string) bool { return true })

	for _, enabled := range []bool{false, true, false} {
		svc.SetBypassSubagents(enabled)
		decision := svc.Check(delegate, input)
		if decision.Allowed != enabled || svc.DelegationEnabled() != enabled {
			t.Fatalf("enabled=%v: decision=%+v, delegation=%v", enabled, decision, svc.DelegationEnabled())
		}
		if !enabled && !strings.Contains(decision.Reason, "permissions.bypass_subagents") {
			t.Fatalf("missing opt-in guidance: %+v", decision)
		}
		if svc.Mode() != ModeBypass || !svc.RequestApproval("test", "test") {
			t.Fatal("toggle reset mode or approver")
		}
		if svc.Check(tool.NewWriteTool(), []byte(`{"path":"x","content":"x"}`)).Allowed {
			t.Fatal("toggle reset session denials")
		}
	}
	// The setting persists across mode changes but only grants access in bypass.
	svc.SetMode(ModeUltra)
	if !svc.DelegationEnabled() || !svc.Check(delegate, input).Allowed {
		t.Fatal("disabling bypass sub-agents affected ultra")
	}
	svc.SetBypassSubagents(true)
	svc.SetMode(ModePlan)
	if svc.DelegationEnabled() || svc.Check(delegate, input).Allowed {
		t.Fatal("bypass opt-in granted delegation in plan")
	}
	svc.SetMode(ModeBypass)
	if !svc.DelegationEnabled() || !svc.Check(delegate, input).Allowed {
		t.Fatal("mode switch lost bypass opt-in")
	}
}

func TestNilServiceDoesNotExposeDelegation(t *testing.T) {
	var svc *Service
	svc.SetBypassSubagents(true)
	if svc.DelegationEnabled() {
		t.Fatal("nil permission service exposed delegation")
	}
}
