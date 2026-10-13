package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSlashHelpIncludesFixSession(t *testing.T) {
	if !strings.Contains(slashHelpText(), "/fix-session") {
		t.Fatalf("expected /fix-session in slash command help")
	}
}

func TestFixSessionIsBuiltinSlashCommand(t *testing.T) {
	if !isBuiltinSlashCommand("fix-session") {
		t.Fatalf("expected /fix-session to be recognized as a built-in command")
	}
}

func TestSlashHelpIncludesWorkflowCommands(t *testing.T) {
	help := slashHelpText()
	if !strings.Contains(help, "/workflow") || !strings.Contains(help, "/workflows") {
		t.Fatalf("expected /workflow and /workflows in slash command help, got %q", help)
	}
}

func TestWorkflowIsBuiltinSlashCommand(t *testing.T) {
	if !isBuiltinSlashCommand("workflow") || !isBuiltinSlashCommand("workflows") {
		t.Fatalf("expected /workflow and /workflows to be built-in commands")
	}
}

func TestSlashAutocompleteIncludesWorkflows(t *testing.T) {
	m := New(nil)
	m.input.SetValue("/work")
	m.updateAutocomplete()
	if m.autocomplete == nil {
		t.Fatal("expected slash autocomplete for /work")
	}
	joined := strings.Join(m.autocomplete.Items, ",")
	if !strings.Contains(joined, "workflows") || !strings.Contains(joined, "workflow") {
		t.Fatalf("expected workflow(s) in autocomplete items, got %q", joined)
	}
}

func TestDirectWorkflowSlashCommand(t *testing.T) {
	if got := workflowSlashCommand("ppt"); got != "ppt-workflow" {
		t.Fatalf("workflowSlashCommand(ppt)=%q, want ppt-workflow", got)
	}
	if got := workflowSlashCommand("ppt-workflow"); got != "ppt-workflow" {
		t.Fatalf("workflowSlashCommand(ppt-workflow)=%q, want ppt-workflow", got)
	}

	m := New(nil)
	m.SetWorkflowNamesFn(func() []string {
		return []string{"ppt", "demo-workflow", "test-then-review"}
	})

	// Any loaded workflow is invokable as /name-workflow.
	if name, ok := m.resolveDirectWorkflowName("ppt-workflow"); !ok || name != "ppt" {
		t.Fatalf("resolve ppt-workflow => (%q,%v), want (ppt,true)", name, ok)
	}
	if name, ok := m.resolveDirectWorkflowName("demo-workflow"); !ok || name != "demo-workflow" {
		t.Fatalf("resolve demo-workflow => (%q,%v), want (demo-workflow,true)", name, ok)
	}
	if name, ok := m.resolveDirectWorkflowName("test-then-review-workflow"); !ok || name != "test-then-review" {
		t.Fatalf("resolve test-then-review-workflow => (%q,%v), want (test-then-review,true)", name, ok)
	}
	if _, ok := m.resolveDirectWorkflowName("missing-workflow"); ok {
		t.Fatal("unloaded workflow should not resolve")
	}
	if m.isDirectWorkflowCommand("test-then-review") {
		t.Fatal("slash form without -workflow suffix should not match")
	}

	// Exact name wins over base name when both exist.
	m.SetWorkflowNamesFn(func() []string {
		return []string{"ppt", "ppt-workflow"}
	})
	if name, ok := m.resolveDirectWorkflowName("ppt-workflow"); !ok || name != "ppt-workflow" {
		t.Fatalf("exact match should win, got (%q,%v)", name, ok)
	}

	m.SetWorkflowNamesFn(func() []string {
		return []string{"ppt", "test-then-review"}
	})
	m.input.SetValue("/ppt")
	m.updateAutocomplete()
	if m.autocomplete == nil {
		t.Fatal("expected autocomplete for /ppt")
	}
	joined := strings.Join(m.autocomplete.Items, ",")
	if !strings.Contains(joined, "ppt-workflow") {
		t.Fatalf("expected ppt-workflow in autocomplete, got %q", joined)
	}

	m.input.SetValue("/test")
	m.updateAutocomplete()
	if m.autocomplete == nil {
		t.Fatal("expected autocomplete for /test")
	}
	joined = strings.Join(m.autocomplete.Items, ",")
	if !strings.Contains(joined, "test-then-review-workflow") {
		t.Fatalf("expected test-then-review-workflow in autocomplete, got %q", joined)
	}
	if strings.Contains(","+joined+",", ",test-then-review,") {
		t.Fatalf("raw workflow name without -workflow must not appear, got %q", joined)
	}
}

func TestSlashHelpIncludesDirectWorkflowShortcut(t *testing.T) {
	help := slashHelpText()
	if !strings.Contains(help, "/[name]-workflow") {
		t.Fatalf("expected direct workflow shortcut in help, got %q", help)
	}
}

func TestShiftTabCyclesPermissionMode(t *testing.T) {
	m := New(nil)
	m.permissionMode = "plan"
	var switched string
	m.modeSwitchFn = func(mode string) { switched = mode }

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	got := updated.(Model)
	if switched != "ultra" {
		t.Fatalf("switched mode = %q, want ultra", switched)
	}
	if got.permissionMode != "ultra" {
		t.Fatalf("permission mode = %q, want ultra", got.permissionMode)
	}
}

func TestSlashAutocompleteIncludesGoal(t *testing.T) {
	m := New(nil)
	m.input.SetValue("/go")
	m.updateAutocomplete()
	if m.autocomplete == nil || !strings.Contains(strings.Join(m.autocomplete.Items, ","), "goal") {
		t.Fatalf("expected goal in slash autocomplete, got %#v", m.autocomplete)
	}
}

func TestCustomProviderDialogCollectsAllFields(t *testing.T) {
	var gotKind DialogKind
	var gotValues []string
	m := New(nil)
	m.SetCustomDialogCallback(func(kind DialogKind, values []string) SelectResult {
		gotKind = kind
		gotValues = append([]string(nil), values...)
		return SelectResult{Message: "saved"}
	})
	m.dialog = &DialogState{Active: DialogProvider}
	m.startCustomDialog()

	for _, value := range []string{"openrouter", "sk-test", "https://example.test/v1", "openai"} {
		m.dialog.CustomInput.SetValue(value)
		updated, _ := m.Update(parseKeyMsg("enter"))
		m = updated.(Model)
	}

	if gotKind != DialogProvider {
		t.Fatalf("custom dialog kind = %v, want provider", gotKind)
	}
	want := []string{"openrouter", "sk-test", "https://example.test/v1", "openai"}
	if strings.Join(gotValues, "|") != strings.Join(want, "|") {
		t.Fatalf("custom provider values = %#v, want %#v", gotValues, want)
	}
	if m.dialog != nil {
		t.Fatal("expected dialog to close after custom provider submission")
	}
}

func TestCustomProviderDialogDefaultsAPIProtocol(t *testing.T) {
	var gotValues []string
	m := New(nil)
	m.SetCustomDialogCallback(func(kind DialogKind, values []string) SelectResult {
		gotValues = append([]string(nil), values...)
		return SelectResult{Message: "saved"}
	})
	m.dialog = &DialogState{Active: DialogProvider}
	m.startCustomDialog()

	for _, value := range []string{"openrouter", "sk-test", "https://example.test/v1", ""} {
		m.dialog.CustomInput.SetValue(value)
		updated, _ := m.Update(parseKeyMsg("enter"))
		m = updated.(Model)
	}

	want := []string{"openrouter", "sk-test", "https://example.test/v1", "anthropic"}
	if strings.Join(gotValues, "|") != strings.Join(want, "|") {
		t.Fatalf("custom provider values = %#v, want %#v", gotValues, want)
	}
}

func TestCustomModelDialogCollectsModelID(t *testing.T) {
	var gotValues []string
	m := New(nil)
	m.SetCustomDialogCallback(func(kind DialogKind, values []string) SelectResult {
		if kind != DialogModel {
			t.Fatalf("custom dialog kind = %v, want model", kind)
		}
		gotValues = append([]string(nil), values...)
		return SelectResult{}
	})
	m.dialog = &DialogState{Active: DialogModel}
	m.startCustomDialog()
	m.dialog.CustomInput.SetValue("vendor/model")

	updated, _ := m.Update(parseKeyMsg("enter"))
	m = updated.(Model)
	if len(gotValues) != 1 || gotValues[0] != "vendor/model" {
		t.Fatalf("custom model values = %#v", gotValues)
	}
	if m.dialog != nil {
		t.Fatal("expected dialog to close after custom model submission")
	}
}
