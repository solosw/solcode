package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
)

func writeACPSkill(t *testing.T, dir, name, extra string) string {
	t.Helper()
	root := filepath.Join(dir, name)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: Run " + name + " workflow\n" + extra + "---\nInstructions for " + name + ".\n"
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func latestACPCommands(client *harnessClient) map[string]AvailableCommand {
	commands := make(map[string]AvailableCommand)
	for _, event := range client.snapshot() {
		if event.Update.SessionUpdate != "available_commands_update" {
			continue
		}
		clear(commands)
		for _, cmd := range event.Update.AvailableCommands {
			commands[cmd.Name] = cmd
		}
	}
	return commands
}

func TestACPSkillsAdvertisedActivatedAndRefreshed(t *testing.T) {
	dir, workDir, sessionDir := t.TempDir(), t.TempDir(), t.TempDir()
	reviewRoot := writeACPSkill(t, dir, "review", "")
	for _, name := range []string{"help", "skill.help", "disabled"} {
		writeACPSkill(t, dir, name, "")
	}
	writeACPSkill(t, dir, "hidden", "user-invocable: false\n")
	prompts := make(chan string, 4)
	mutate := func(cfg config.Config) config.Config {
		cfg.WorkDir, cfg.Session.Dir, cfg.Skills.Paths = workDir, sessionDir, []string{dir}
		cfg.Skills.Disabled = []string{"disabled"}
		return cfg
	}
	run := func(_ context.Context, _ *app.App, _, prompt, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		prompts <- prompt
		return agent.AgentResult{Output: "ok"}, nil
	}
	client := newACPHarnessWithConfig(t, mutate, run)
	created := createConfiguredSession(t, client, workDir)
	waitFor(t, func() bool { _, ok := latestACPCommands(client)["review"]; return ok })
	for _, name := range []string{"review", "help", "skill.help", "skill.skill.help"} {
		if _, ok := latestACPCommands(client)[name]; !ok {
			t.Fatalf("skill or builtin missing: %s", name)
		}
	}
	for _, name := range []string{"hidden", "disabled"} {
		if _, ok := latestACPCommands(client)[name]; ok {
			t.Fatalf("unavailable skill advertised: %s", name)
		}
	}
	if latestACPCommands(client)["review"].Description != "Run review workflow" {
		t.Fatal("skill description missing")
	}
	prompt := func(text string) {
		mustCall(t, client, MethodSessionPrompt, SessionPromptParams{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: text}}})
	}
	prompt("/review\ncheck the API")
	got := <-prompts
	for _, want := range []string{"/review", "check the API", "[Skill: review]", "Instructions for review.", "Root: " + reviewRoot, "explicitly invoked"} {
		if !strings.Contains(got, want) {
			t.Fatalf("activated prompt missing %q: %s", want, got)
		}
	}
	prompt("/skill.skill.help explain this")
	if got := <-prompts; !strings.Contains(got, "[Skill: help]") {
		t.Fatalf("qualified collision invoked wrong skill: %s", got)
	}
	prompt("/help")
	select {
	case <-prompts:
		t.Fatal("builtin help was shadowed by skill")
	default:
	}
	prompt("/unknown normal chat")
	if got := <-prompts; got != "/unknown normal chat" {
		t.Fatalf("unknown command changed: %s", got)
	}
	var response SessionPromptResult
	err := client.conn.Call(t.Context(), MethodSessionPrompt, map[string]any{"sessionId": created.SessionID, "prompt": []map[string]any{{"type": "skill", "name": "review"}, {"type": "text", "text": "do this"}}}, &response)
	requireRPCError(t, err, JSONRPCInvalidParams)
	select {
	case <-prompts:
		t.Fatal("unsupported structured block was inserted")
	default:
	}
	// Loading an empty session re-publishes the active skill catalog as v1 commands.
	client.server.Close()
	client = newACPHarnessWithConfig(t, mutate, run)
	mustCall(t, client, MethodInitialize, InitializeParams{ProtocolVersion: 1})
	mustCall(t, client, MethodSessionLoad, SessionLoadParams{SessionID: created.SessionID, Cwd: workDir})
	prompt("/review after load")
	if got := <-prompts; !strings.Contains(got, "Instructions for review.") {
		t.Fatal("loaded session lost skill activation")
	}
	client.mu.Lock()
	client.permissionOption = "review"
	client.mu.Unlock()
	prompt("/skills")
	if _, ok := latestACPCommands(client)["review"]; ok {
		t.Fatal("disabled skill remained in the client command list")
	}
	prompt("/skills")
	if _, ok := latestACPCommands(client)["review"]; !ok {
		t.Fatal("re-enabled skill missing from client command list")
	}
	// v1 command payloads stay compatible with closed command/content unions.
	wire, err := json.Marshal(SessionUpdate{SessionUpdate: "available_commands_update", AvailableCommands: client.server.getSession(created.SessionID).availableCommands()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), `"type":"skill"`) {
		t.Fatal("v2 command entry sent to v1 client")
	}
}
