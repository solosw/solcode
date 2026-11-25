package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/attach"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
)

type v2TestUpdate struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		Kind       string          `json:"sessionUpdate"`
		MessageID  string          `json:"messageId"`
		State      string          `json:"state"`
		StopReason string          `json:"stopReason"`
		Error      *jsonrpcError   `json:"error"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
	} `json:"update"`
}

func (c *harnessClient) v2Snapshot() []v2TestUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]v2TestUpdate(nil), c.v2Updates...)
}

func initializeV2Test(t *testing.T, c *harnessClient, skills bool) {
	t.Helper()
	params := map[string]any{"protocolVersion": 2, "info": map[string]string{"name": "test-client", "version": "1"}}
	if skills {
		params["capabilities"] = map[string]any{"session": map[string]any{"commands": map[string]any{"skill": struct{}{}}}}
	}
	var result struct {
		ProtocolVersion int                `json:"protocolVersion"`
		Info            ImplementationInfo `json:"info"`
		Capabilities    struct {
			Session struct {
				Prompt struct {
					Skill struct {
						Inline *struct{} `json:"inline"`
					} `json:"skill"`
				} `json:"prompt"`
			} `json:"session"`
		} `json:"capabilities"`
	}
	raw := mustCall(t, c, MethodInitialize, params)
	if err := json.Unmarshal(raw, &result); err != nil || result.ProtocolVersion != 2 || result.Info.Name != "solcode" || result.Capabilities.Session.Prompt.Skill.Inline == nil {
		t.Fatalf("v2 initialize = %s (%v)", raw, err)
	}
}

func createV2TestSession(t *testing.T, c *harnessClient, cwd string) v2SessionResult {
	t.Helper()
	raw := mustCall(t, c, MethodSessionNew, SessionNewParams{Cwd: cwd})
	var result v2SessionResult
	if err := json.Unmarshal(raw, &result); err != nil || result.SessionID == "" || len(result.ConfigOptions) == 0 || result.ConfigOptions[0].ConfigID == "" {
		t.Fatalf("v2 session/new = %s (%v)", raw, err)
	}
	return result
}

func v2TestPrompt(t *testing.T, c *harnessClient, p map[string]any) string {
	t.Helper()
	raw := mustCall(t, c, MethodSessionPrompt, p)
	var result v2PromptResult
	if err := json.Unmarshal(raw, &result); err != nil || result.MessageID == "" || strings.Contains(string(raw), "stopReason") {
		t.Fatalf("v2 session/prompt = %s (%v)", raw, err)
	}
	return result.MessageID
}

func TestACPV2SkillPromptResumeAndDeduplication(t *testing.T) {
	skills, cwd, store := t.TempDir(), t.TempDir(), t.TempDir()
	review := writeACPSkill(t, skills, "review", "")
	writeACPSkill(t, skills, "help", "")
	writeACPSkill(t, skills, "disabled", "")
	writeACPSkill(t, skills, "hidden", "user-invocable: false\n")
	mutate := func(cfg config.Config) config.Config {
		cfg.WorkDir, cfg.Session.Dir, cfg.Skills.Paths = cwd, store, []string{skills}
		cfg.Skills.Disabled = []string{"disabled"}
		return cfg
	}
	started := make(chan string, 2)
	proceed := make(chan struct{})
	run := func(ctx context.Context, application *app.App, id, prompt, workDir string, _ int, emit StreamEmitter) (agent.AgentResult, error) {
		started <- prompt
		select {
		case <-proceed:
		case <-ctx.Done():
			return agent.AgentResult{}, ctx.Err()
		}
		current, err := application.Sessions.Load(ctx, session.SessionID(id))
		if err != nil {
			return agent.AgentResult{}, err
		}
		current.Append(attach.UserMessage(attach.Expand(prompt, workDir)))
		current.Append(sdk.NewAssistantMessage(sdk.NewTextBlock("result")))
		if err := application.Sessions.Save(ctx, current); err != nil {
			return agent.AgentResult{}, err
		}
		emit.Text("res")
		emit.Text("ult")
		emit.ToolStart("Glob", json.RawMessage(`{"pattern":"*.go"}`), "toolu_glob")
		emit.ToolDone("Glob", "done", false, "toolu_glob")
		emit.Text("finished")
		return agent.AgentResult{Output: "result"}, nil
	}
	client := newACPHarnessWithConfig(t, mutate, run)
	initializeV2Test(t, client, true)
	created := createV2TestSession(t, client, cwd)
	commands := map[string]v2Command{}
	for _, cmd := range created.AvailableCommands {
		commands[cmd.Name] = cmd
	}
	if cmd := commands["review"]; cmd.Type != "skill" || cmd.Path != filepath.Join(review, "SKILL.md") || cmd.Description == "" || cmd.Input != nil {
		t.Fatalf("review command = %+v", cmd)
	}
	if commands["help"].Type != "command" || commands["skill.help"].Type != "skill" || commands["hidden"].Type != "" || commands["disabled"].Type != "" {
		t.Fatalf("unexpected catalog: %+v", commands)
	}
	for _, name := range []string{"hidden", "disabled", "help", "review ", "missing"} {
		err := client.conn.Call(t.Context(), MethodSessionPrompt, map[string]any{
			"sessionId": created.SessionID, "idempotencyKey": "submission", "prompt": []ContentBlock{{Type: "skill", Name: name}},
		}, nil)
		requireRPCError(t, err, JSONRPCInvalidParams)
	}
	if updates := client.v2Snapshot(); len(updates) != 0 {
		t.Fatalf("invalid prompts emitted updates: %+v", updates)
	}
	blocks := []ContentBlock{{Type: "text", Text: "Before"}, {Type: "skill", Name: "review"}, {Type: "text", Text: "Between"}, {Type: "skill", Name: "skill.help"}, {Type: "text", Text: "After"}}
	request := map[string]any{"sessionId": created.SessionID, "idempotencyKey": "submission", "prompt": blocks}
	messageID := v2TestPrompt(t, client, request)
	if got := v2TestPrompt(t, client, request); got != messageID {
		t.Fatalf("in-flight retry = %q, want %q", got, messageID)
	}
	select {
	case rendered := <-started:
		for _, piece := range []string{"Before", "Instructions for review.", "Between", "Instructions for help.", "After"} {
			pos := strings.Index(rendered, piece)
			if pos < 0 {
				t.Fatalf("could not find %q at expected position in %q", piece, rendered)
			}
			rendered = rendered[pos+len(piece):]
		}
	case <-t.Context().Done():
		t.Fatal("model did not receive the prompt")
	}
	close(proceed)
	waitFor(t, func() bool {
		for _, event := range client.v2Snapshot() {
			if event.Update.State == "idle" && event.Update.StopReason == StopReasonEndTurn {
				return true
			}
		}
		return false
	})
	assertReplay := func(c *harnessClient, expected []ContentBlock) {
		t.Helper()
		messages := 0
		for _, event := range c.v2Snapshot() {
			if event.Update.Kind != "user_message" {
				continue
			}
			messages++
			var got []ContentBlock
			if err := json.Unmarshal(event.Update.Content, &got); err != nil || event.Update.MessageID != messageID || !reflect.DeepEqual(got, expected) {
				t.Fatalf("unexpected user message %s with id %s (%v)", event.Update.Content, event.Update.MessageID, err)
			}
		}
		if messages != 1 {
			t.Fatalf("got %d user messages", messages)
		}
	}
	assertReplay(client, blocks)
	var ids []string
	tools := 0
	for _, event := range client.v2Snapshot() {
		switch event.Update.Kind {
		case "agent_message_chunk":
			ids = append(ids, event.Update.MessageID)
		case "tool_call_update":
			tools++
			if event.Update.ToolCallID == "" {
				t.Fatal("tool call patch missing ID")
			}
		}
	}
	if len(ids) != 3 || ids[0] == "" || ids[0] != ids[1] || ids[0] == ids[2] || tools != 2 {
		t.Fatalf("message IDs %v, tool patches %d", ids, tools)
	}
	mustCall(t, client, "session/close", SessionCancelParams{SessionID: created.SessionID})
	resume := map[string]any{"sessionId": created.SessionID, "cwd": cwd, "replayFrom": map[string]string{"type": "start"}}
	for _, structured := range []bool{true, false} {
		reopened := newACPHarnessWithConfig(t, mutate, run)
		initializeV2Test(t, reopened, structured)
		raw := mustCall(t, reopened, "session/resume", resume)
		var result v2SessionResult
		if err := json.Unmarshal(raw, &result); err != nil || len(result.AvailableCommands) == 0 {
			t.Fatalf("session/resume = %s (%v)", raw, err)
		}
		expected := append([]ContentBlock(nil), blocks...)
		if !structured {
			expected[1] = ContentBlock{Type: "text", Text: "/review"}
			expected[3] = ContentBlock{Type: "text", Text: "/skill.help"}
			for _, command := range result.AvailableCommands {
				if command.Type != "command" {
					t.Fatalf("fallback command = %+v", command)
				}
			}
		}
		assertReplay(reopened, expected)
		got := reopened.v2Snapshot()
		if len(got) < 2 || got[len(got)-2].Update.Kind != "replay_complete" || got[len(got)-1].Update.State != "idle" {
			t.Fatalf("resume did not complete replay before reporting idle: %+v", got)
		}
		if retryID := v2TestPrompt(t, reopened, request); retryID != messageID {
			t.Fatalf("persisted retry returned %q, want %q", retryID, messageID)
		}
		mustCall(t, reopened, "session/close", SessionCancelParams{SessionID: created.SessionID})
	}
	legacy := newACPHarnessWithConfig(t, mutate, nil)
	mustCall(t, legacy, MethodInitialize, InitializeParams{ProtocolVersion: 1})
	mustCall(t, legacy, MethodSessionLoad, SessionLoadParams{SessionID: created.SessionID, Cwd: cwd})
	if !hasText(legacy.snapshot(), "user_message_chunk", "/review") || hasText(legacy.snapshot(), "user_message_chunk", "Instructions for review.") {
		t.Fatal("v1 replay did not translate the structured skill to a slash reference")
	}
	select {
	case got := <-started:
		t.Fatalf("retry invoked the model: %q", got)
	default:
	}
}

func TestACPV2CloseCancelsAndResumeEmptySession(t *testing.T) {
	cwd := t.TempDir()
	started := make(chan struct{})
	client := newACPHarness(t, func(ctx context.Context, _ *app.App, _, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		close(started)
		<-ctx.Done()
		return agent.AgentResult{}, ctx.Err()
	})
	initializeV2Test(t, client, false)
	created := createV2TestSession(t, client, cwd)
	params := SessionCancelParams{SessionID: created.SessionID}
	listParams := map[string]string{"cwd": cwd}
	if raw := mustCall(t, client, MethodSessionList, listParams); string(raw) != `{"sessions":[]}` {
		t.Fatalf("session/list exposed an empty session: %s", raw)
	}
	option := created.ConfigOptions[0]
	if raw := mustCall(t, client, MethodSessionSetConfigOption, SessionSetConfigOptionParams{
		SessionID: created.SessionID, ConfigID: option.ConfigID, Type: "id", Value: option.CurrentValue,
	}); !strings.Contains(string(raw), `"configId":"mode"`) {
		t.Fatalf("session/set_config_option = %s", raw)
	}
	before := len(client.v2Snapshot())
	mustCall(t, client, "session/close", params)
	mustCall(t, client, "session/resume", map[string]any{"sessionId": created.SessionID, "cwd": cwd, "replayFrom": map[string]string{"type": "start"}})
	updates := client.v2Snapshot()[before:]
	if len(updates) != 2 || updates[0].Update.Kind != "replay_complete" || updates[1].Update.State != "idle" {
		t.Fatalf("empty session replay = %+v", updates)
	}
	v2TestPrompt(t, client, map[string]any{"sessionId": created.SessionID, "prompt": []ContentBlock{{Type: "text", Text: "hello"}}})
	<-started
	// This runner never writes a transcript; the durable accepted prompt is replayable.
	var listed struct {
		Sessions []struct {
			ID string `json:"sessionId"`
		} `json:"sessions"`
	}
	raw := mustCall(t, client, MethodSessionList, listParams)
	if err := json.Unmarshal(raw, &listed); err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].ID != created.SessionID {
		t.Fatalf("session/list = %s (%v), want the session with a saved prompt", raw, err)
	}
	mustCall(t, client, "session/close", params)
	updates = client.v2Snapshot()
	if last := updates[len(updates)-1].Update; last.State != "idle" || last.StopReason != StopReasonCancelled {
		t.Fatalf("close returned before terminal cancellation state: %+v", last)
	}
	err := client.conn.Call(t.Context(), MethodSessionPrompt, map[string]any{"sessionId": created.SessionID, "prompt": []ContentBlock{{Type: "text", Text: "hello"}}}, nil)
	requireRPCError(t, err, v2SessionNotFound)
}

func TestACPV2SkillReachesModelAndReplaysOriginalBlocks(t *testing.T) {
	skills, cwd, store := t.TempDir(), t.TempDir(), t.TempDir()
	writeACPSkill(t, skills, "review", "")
	requests := make(chan string, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		select {
		case requests <- string(body):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"v2-response","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"checked"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer endpoint.Close()
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.WorkDir, cfg.Session.Dir, cfg.Skills.Paths = cwd, store, []string{skills}
		cfg.BaseURL, cfg.APIFormat, cfg.Model = endpoint.URL, "anthropic", "test"
		cfg.Stream = false
		disabled := false
		cfg.CodeGraph.Enabled = &disabled
		return cfg
	}, nil)
	initializeV2Test(t, client, true)
	created := createV2TestSession(t, client, cwd)
	id := created.SessionID
	block := ContentBlock{Type: "skill", Name: "review"}
	messageID := v2TestPrompt(t, client, map[string]any{"sessionId": id, "prompt": []ContentBlock{block}})
	waitFor(t, func() bool {
		for _, event := range client.v2Snapshot() {
			if event.Update.State == "idle" {
				if event.Update.StopReason != StopReasonEndTurn {
					t.Fatalf("default prompt did not complete: %+v", event.Update)
				}
				return true
			}
		}
		return false
	})
	select {
	case body := <-requests:
		if !strings.Contains(body, "Instructions for review.") {
			t.Fatal("upstream prompt did not contain activated skill")
		}
	default:
		t.Fatal("model was not called")
	}
	records := client.server.getSession(id).v2Prompts
	if len(records) != 1 || records[0].Fingerprint == "" {
		t.Fatalf("prompt was not linked to the stored model input: %+v", records)
	}
	mustCall(t, client, "session/close", SessionCancelParams{SessionID: id})
	before := len(client.v2Snapshot())
	mustCall(t, client, "session/resume", map[string]any{"sessionId": id, "cwd": cwd, "replayFrom": map[string]string{"type": "start"}})
	users := 0
	for _, event := range client.v2Snapshot()[before:] {
		if event.Update.Kind != "user_message" {
			continue
		}
		users++
		var got []ContentBlock
		if err := json.Unmarshal(event.Update.Content, &got); err != nil || event.Update.MessageID != messageID || !reflect.DeepEqual(got, []ContentBlock{block}) {
			t.Fatalf("model input leaked or skill reference lost: %s (%v)", event.Update.Content, err)
		}
	}
	if users != 1 {
		t.Fatalf("expected one replayed user message, got %d", users)
	}
}

func TestACPV2AcceptedPromptFailureReportsState(t *testing.T) {
	client := newACPHarness(t, func(context.Context, *app.App, string, string, string, int, StreamEmitter) (agent.AgentResult, error) {
		return agent.AgentResult{}, errors.New("model unavailable")
	})
	initializeV2Test(t, client, false)
	created := createV2TestSession(t, client, t.TempDir())
	v2TestPrompt(t, client, map[string]any{"sessionId": created.SessionID, "prompt": []ContentBlock{{Type: "text", Text: "hello"}}})
	waitFor(t, func() bool {
		for _, event := range client.v2Snapshot() {
			if event.Update.State == "idle" {
				if event.Update.StopReason != "error" || event.Update.Error == nil || event.Update.Error.Message != "model unavailable" {
					t.Fatalf("idle failure = %+v", event.Update)
				}
				return true
			}
		}
		return false
	})
}
