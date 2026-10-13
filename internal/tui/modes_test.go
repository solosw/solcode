package tui

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/solosw/solcode/internal/permission"
)

func TestDefaultModeCycleIncludesUltraAndWraps(t *testing.T) {
	m := New(nil)
	var switched []string
	m.SetModeSwitchFn(nil, "", func(mode string) { switched = append(switched, mode) })
	wantNames := permission.CycleModeNames()
	if !reflect.DeepEqual(m.modeNames, wantNames) || m.permissionMode != wantNames[0] {
		t.Fatalf("default modes = %v, current = %q", m.modeNames, m.permissionMode)
	}
	for range wantNames {
		m.cyclePermissionMode()
	}
	want := append(append([]string(nil), wantNames[1:]...), wantNames[0])
	if !reflect.DeepEqual(switched, want) || m.permissionMode != "auto" {
		t.Fatalf("cycle = %v, want %v; current = %q", switched, want, m.permissionMode)
	}
}

func TestLegacyModeSwitchNamesNormalizeAndDeduplicate(t *testing.T) {
	m := New(nil)
	names := []string{"auto", "ultrl", "ultra"}
	var switched string
	m.SetModeSwitchFn(names, " UlTrL ", func(mode string) { switched = mode })
	if !reflect.DeepEqual(m.modeNames, []string{"auto", "ultra"}) || m.permissionMode != "ultra" {
		t.Fatalf("modes = %v, current = %q", m.modeNames, m.permissionMode)
	}
	names[0] = "plan"
	m.cyclePermissionMode()
	if switched != "auto" {
		t.Fatalf("cycled to %q, want auto", switched)
	}
}

func TestModelModeUpdateSynchronizesDisplayWithoutSwitchingAgain(t *testing.T) {
	m := New(nil)
	switches := 0
	m.SetModeSwitchFn(nil, "plan", func(string) { switches++ })
	updated, _ := m.Update(PermissionModeChangedMsg{Mode: "ultrl"})
	m = updated.(Model)
	if m.permissionMode != "ultra" || switches != 0 {
		t.Fatalf("display mode = %q, switches = %d", m.permissionMode, switches)
	}
	for _, invalid := range []string{"", " ", "unknown"} {
		updated, _ = m.Update(PermissionModeChangedMsg{Mode: invalid})
		m = updated.(Model)
		if m.permissionMode != "ultra" || switches != 0 {
			t.Fatalf("invalid update changed mode to %q, switches = %d", m.permissionMode, switches)
		}
	}
	m.cyclePermissionMode()
	if m.permissionMode != "auto" || switches != 1 {
		t.Fatalf("cycle after model update = %q, switches = %d", m.permissionMode, switches)
	}
}

func TestWorkflowCommandsDoNotChangeDisplayedPermissionMode(t *testing.T) {
	for _, mode := range []string{"plan", "ultra"} {
		for _, command := range []string{"/workflow demo", "/demo-workflow"} {
			t.Run(mode+command, func(t *testing.T) {
				m := New(nil)
				m.SetModeSwitchFn(nil, mode, func(string) { t.Fatal("workflow implicitly changed permission mode") })
				m.SetWorkflowNamesFn(func() []string { return []string{"demo"} })
				m.SetSlashCommandAsyncHandler(func(string, string) tea.Cmd { return nil })
				handled, _ := m.handleSlashCommand(command)
				if !handled || m.permissionMode != mode {
					t.Fatalf("command handled = %v, mode = %q, want %q", handled, m.permissionMode, mode)
				}
			})
		}
	}
}
