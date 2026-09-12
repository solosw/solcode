package acp

import (
	"context"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

func TestSlashHelpListsStatus(t *testing.T) {
	if !strings.Contains(slashHelpText(), "/status") {
		t.Fatal("help should list /status")
	}
}

func TestBuiltinIncludesProxy(t *testing.T) {
	if !isBuiltinSlashCommand("proxy") {
		t.Fatal("proxy should be a builtin slash command")
	}
	if !strings.Contains(slashHelpText(), "/proxy") {
		t.Fatal("help should list /proxy")
	}
	found := false
	for _, cmd := range availableCommands() {
		if cmd.Name == "proxy" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("availableCommands missing proxy")
	}
}

func TestBuiltinIncludesCheckpointCommands(t *testing.T) {
	for _, name := range []string{"checkpoints", "checkpoint-name", "rewind"} {
		if !isBuiltinSlashCommand(name) {
			t.Fatalf("%s should be a builtin slash command", name)
		}
		if !strings.Contains(slashHelpText(), "/"+name) {
			t.Fatalf("help should list /%s", name)
		}
		found := false
		for _, cmd := range availableCommands() {
			if cmd.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("availableCommands missing %s", name)
		}
	}
}

func TestBuiltinIncludesStatus(t *testing.T) {
	if !isBuiltinSlashCommand("status") {
		t.Fatal("status should be a builtin slash command")
	}
}

func TestAvailableCommandsIncludesStatus(t *testing.T) {
	found := false
	for _, cmd := range availableCommands() {
		if cmd.Name == "status" {
			found = true
			if !strings.Contains(strings.ToLower(cmd.Description), "cache") &&
				!strings.Contains(strings.ToLower(cmd.Description), "context") {
				t.Fatalf("status description = %q", cmd.Description)
			}
			break
		}
	}
	if !found {
		t.Fatal("availableCommands missing status")
	}
}

func TestSlashStatusFormatsUsage(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	manager := session.NewManager(store, "main")
	sessData := session.NewSession(session.SessionID("main"), "/work", "claude-sonnet")
	sessData.Metadata.Usage = session.UsageStats{
		InputTokens:              1_000,
		OutputTokens:             250,
		CacheCreationInputTokens: 500,
		CacheReadInputTokens:     4_000,
	}
	if err := manager.Save(context.Background(), sessData); err != nil {
		t.Fatal(err)
	}

	application := &app.App{
		Sessions: manager,
		Config: config.Config{
			Model:            "claude-sonnet",
			Provider:         "anthropic",
			Effort:           "high",
			MaxContextTokens: 200_000,
			WorkDir:          "/work",
		},
	}
	s := &Server{}
	sess := &acpSession{
		id:          "acp-1",
		diskID:      "main",
		workDir:     "/work",
		cfg:         application.Config,
		application: application,
	}

	msg, err := s.slashStatus(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Model: claude-sonnet",
		"Provider: anthropic",
		"Effort: high",
		"Session: main",
		"Workdir: /work",
		"Proxy:",
		"Context:",
		"Cache:",
		"read 4k",
		"write 500",
		"Output: 250",
		"Input: 1k uncached",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("status missing %q\n%s", want, msg)
		}
	}
}

func TestCompactStatusTokens(t *testing.T) {
	if got := compactStatusTokens(12_500); got != "12.5k" {
		t.Fatalf("compactStatusTokens(12500)=%q", got)
	}
	if got := compactStatusTokens(200_000); got != "200k" {
		t.Fatalf("compactStatusTokens(200000)=%q", got)
	}
	if got := renderStatusLimit(0); got != "?" {
		t.Fatalf("renderStatusLimit(0)=%q", got)
	}
}
