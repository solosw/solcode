package tui

import (
	"strings"
	"testing"
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

	for _, value := range []string{"openrouter", "sk-test", "https://example.test/v1"} {
		m.dialog.CustomInput.SetValue(value)
		updated, _ := m.Update(parseKeyMsg("enter"))
		m = updated.(Model)
	}

	if gotKind != DialogProvider {
		t.Fatalf("custom dialog kind = %v, want provider", gotKind)
	}
	want := []string{"openrouter", "sk-test", "https://example.test/v1"}
	if strings.Join(gotValues, "|") != strings.Join(want, "|") {
		t.Fatalf("custom provider values = %#v, want %#v", gotValues, want)
	}
	if m.dialog != nil {
		t.Fatal("expected dialog to close after custom provider submission")
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
