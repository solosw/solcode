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
