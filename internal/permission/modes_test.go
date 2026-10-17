package permission

import (
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestNormalizeUltraModeAliases(t *testing.T) {
	for _, input := range []Mode{"ultra", "ultrl", " ULTRA ", " UlTrL ", ModeUltrl} {
		if got := NormalizeMode(input); got != ModeUltra || string(got) != "ultra" {
			t.Errorf("NormalizeMode(%q) = %q, want ultra", input, got)
		}
	}
}

func TestAvailableModesAreCanonicalAndUnique(t *testing.T) {
	seen := make(map[Mode]bool)
	for _, info := range AvailableModes() {
		if seen[info.ID] || NormalizeMode(info.ID) != info.ID || info.Name == "" || info.Description == "" {
			t.Fatalf("invalid mode catalog entry: %+v", info)
		}
		seen[info.ID] = true
	}
	if len(seen) != 6 || !seen[ModeUltra] || seen[Mode("ultrl")] {
		t.Fatalf("mode catalog = %v, want six canonical modes including ultra", seen)
	}
	for _, input := range []Mode{ModeUltra, "ultrl", " ULTRA ", "default", "yolo", "acceptedits"} {
		if !IsSupportedMode(input) {
			t.Errorf("mode %q should be supported", input)
		}
	}
	for _, input := range []Mode{"", " ", "unknown"} {
		if IsSupportedMode(input) {
			t.Errorf("mode %q should be rejected", input)
		}
	}
	var ultraInCycle bool
	for _, name := range CycleModeNames() {
		if !seen[Mode(name)] || Mode(name) == ModeGoal {
			t.Fatalf("unexpected cycling mode %q", name)
		}
		ultraInCycle = ultraInCycle || Mode(name) == ModeUltra
	}
	if !ultraInCycle {
		t.Fatal("mode cycle is missing ultra")
	}
}

func TestDelegationRequiresBypassOrUltraEvenWhenAllowlisted(t *testing.T) {
	for _, mode := range []Mode{ModeAuto, ModeAcceptEdits, ModeBypass, ModePlan, ModeGoal, ModeUltra, "ultrl"} {
		t.Run(string(mode), func(t *testing.T) {
			svc := NewService(mode)
			for _, delegate := range []tool.Tool{tool.NewTaskTool(nil), tool.NewSubagentTool(nil)} {
				svc.AllowTool(delegate.Name())
				decision := svc.Check(delegate, []byte(`{"action":"spawn","agent_type":"explorer"}`))
				if want := NormalizeMode(mode) == ModeUltra || NormalizeMode(mode) == ModeBypass; decision.Allowed != want {
					t.Errorf("%s in %s: allowed = %v, want %v (%s)", delegate.Name(), mode, decision.Allowed, want, decision.Reason)
				}
			}
		})
	}
}
