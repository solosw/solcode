package permission

import (
	"github.com/solosw/solcode/internal/tool"
	"testing"
)

func TestBypassRejectsNonBuiltinRolesBeforeAllowlist(t *testing.T) {
	for _, delegate := range []tool.Tool{tool.NewTaskTool(nil), tool.NewSubagentTool(nil)} {
		svc := NewService(ModeBypass)
		svc.AllowTool(delegate.Name())
		for _, input := range []string{
			`{"agent_type":"custom"}`,
			`{"tasks":[{"agent_type":"worker"},{"agent_type":"custom"}]}`,
			`{"agent_type":"../worker"}`,
			`{invalid`,
		} {
			if decision := svc.Check(delegate, []byte(input)); decision.Allowed {
				t.Errorf("%s accepted %s", delegate.Name(), input)
			}
		}
		for _, input := range []string{
			`{"agent_type":"explorer"}`,
			`{"agent_type":"worker"}`,
			`{"agent_type":"reviewer"}`,
			`{"action":"resume","agent_id":"agent-a81f","prompt":"continue"}`,
			`{"action":"list"}`,
			`{"tasks":[{"agent_type":"worker"},{"agent_type":"explorer"}]}`,
		} {
			if decision := svc.Check(delegate, []byte(input)); !decision.Allowed {
				t.Errorf("%s rejected %s: %s", delegate.Name(), input, decision.Reason)
			}
		}
		if decision := NewService(ModeUltra).Check(delegate, []byte(`{"agent_type":"custom"}`)); !decision.Allowed {
			t.Errorf("ultra rejected custom role: %s", decision.Reason)
		}
	}
}
