package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func TestACPInitializeAndPromptStream(t *testing.T) {
	client := newACPHarness(t, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
		if emit.Thinking != nil {
			emit.Thinking("considering")
		}
		if emit.Text != nil {
			emit.Text("hello-back")
		}
		if emit.ToolStart != nil {
			emit.ToolStart("Glob", json.RawMessage(`{"pattern":"*.go"}`), "toolu_glob")
		}
		if emit.ToolDone != nil {
			emit.ToolDone("Glob", "ok", false, "toolu_glob")
		}
		if emit.Usage != nil {
			emit.Usage(engine.Usage{InputTokens: 3, OutputTokens: 2})
		}
		if emit.Status != nil {
			emit.Status("Ready")
		}
		if emit.AgentProgress != nil {
			emit.AgentProgress(tool.AgentProgressEvent{
				Kind:            "started",
				AgentID:         "task-9",
				ParentToolUseID: "toolu_task",
				TaskID:          "a",
				Description:     "Explore files",
			})
			emit.AgentProgress(tool.AgentProgressEvent{
				Kind:            "tool_start",
				AgentID:         "task-9",
				ParentToolUseID: "toolu_task",
				TaskID:          "a",
				Description:     "Explore files",
				ToolName:        "View",
				ToolInput:       `{"path":"README.md"}`,
			})
			emit.AgentProgress(tool.AgentProgressEvent{
				Kind:            "completed",
				AgentID:         "task-9",
				ParentToolUseID: "toolu_task",
				TaskID:          "a",
				Description:     "Explore files",
				Output:          "done",
			})
		}
		return agent.AgentResult{Output: "hello-back"}, nil
	})

	init := mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	if !strings.Contains(string(init), `"name":"solcode"`) {
		t.Fatalf("initialize = %s", init)
	}
	created := mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()})
	var newResult SessionNewResult
	if err := json.Unmarshal(created, &newResult); err != nil {
		t.Fatalf("decode session/new: %v", err)
	}
	if newResult.SessionID == "" || newResult.Modes == nil {
		t.Fatalf("session/new = %+v", newResult)
	}

	mustCall(t, client, MethodSessionPrompt, map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": "hello"}},
	})
	waitFor(t, func() bool {
		snap := client.snapshot()
		return hasKind(snap, "agent_message_chunk") &&
			hasKind(snap, "agent_thought_chunk") &&
			hasKind(snap, "tool_call") &&
			hasKind(snap, "tool_call_update") &&
			hasKind(snap, "usage_update") &&
			hasCommands(snap, "help", "model", "sessions", "new-session")
	})
}

func TestACPPermissionCancelAndMode(t *testing.T) {
	started := make(chan struct{})
	client := newACPHarness(t, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
		close(started)
		if application == nil || application.Permissions == nil {
			return agent.AgentResult{Error: "no permissions"}, nil
		}
		if !application.Permissions.RequestApproval("Bash", "run ls") {
			return agent.AgentResult{Error: "denied"}, nil
		}
		select {
		case <-ctx.Done():
			return agent.AgentResult{Error: context.Canceled.Error()}, ctx.Err()
		case <-time.After(2 * time.Second):
			return agent.AgentResult{Output: "late"}, nil
		}
	})
	client.permissionOption = PermissionAllowOnce

	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	created := mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()})
	var newResult SessionNewResult
	if err := json.Unmarshal(created, &newResult); err != nil {
		t.Fatalf("decode session/new: %v", err)
	}

	promptDone := make(chan json.RawMessage, 1)
	go func() {
		promptDone <- mustCall(t, client, MethodSessionPrompt, map[string]any{
			"sessionId": newResult.SessionID,
			"prompt":    []map[string]any{{"type": "text", "text": "run"}},
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not start")
	}
	mustNotify(t, client, MethodSessionCancel, map[string]any{"sessionId": newResult.SessionID})
	select {
	case raw := <-promptDone:
		var result SessionPromptResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("decode prompt result: %v", err)
		}
		if result.StopReason != StopReasonCancelled {
			t.Fatalf("stopReason = %q, want cancelled", result.StopReason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not finish after cancel")
	}

	mustCall(t, client, MethodSessionSetMode, map[string]any{
		"sessionId": newResult.SessionID,
		"modeId":    string(permission.ModePlan),
	})
	waitFor(t, func() bool {
		return hasMode(client.snapshot(), string(permission.ModePlan))
	})
}

func TestACPSessionLoadAndImagePrompt(t *testing.T) {
	workDir := t.TempDir()
	sessionDir := t.TempDir()
	store := session.NewFileStore(sessionDir)
	existing := session.NewSession("loaded", workDir, "test-model")
	existing.Append(sdk.NewUserMessage(sdk.NewTextBlock("previous question")))
	existing.Append(sdk.NewAssistantMessage(sdk.NewTextBlock("previous answer")))
	if err := store.Save(context.Background(), existing); err != nil {
		t.Fatalf("save session: %v", err)
	}

	var seenPrompt string
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.WorkDir = workDir
		cfg.Session.Enabled = true
		cfg.Session.Persist = true
		cfg.Session.Dir = sessionDir
		cfg.Memory.Enabled = false
		cfg.KnowledgeGraph.Enabled = false
		return cfg
	}, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
		seenPrompt = prompt
		return agent.AgentResult{Output: "ok"}, nil
	})

	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	mustCall(t, client, MethodSessionLoad, map[string]any{
		"sessionId": "loaded",
		"cwd":       workDir,
	})
	waitFor(t, func() bool {
		snap := client.snapshot()
		return hasText(snap, "user_message_chunk", "previous question") && hasText(snap, "agent_message_chunk", "previous answer")
	})

	png := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	mustCall(t, client, MethodSessionPrompt, map[string]any{
		"sessionId": "loaded",
		"prompt": []map[string]any{
			{"type": "text", "text": "describe"},
			{"type": "image", "mimeType": "image/png", "data": png, "name": "shot.png"},
			{"type": "resource_link", "uri": "file://README.md", "name": "README"},
		},
	})
	if !strings.Contains(seenPrompt, "describe") || !strings.Contains(seenPrompt, "@") || !strings.Contains(seenPrompt, "README") {
		t.Fatalf("prompt = %q", seenPrompt)
	}
	matches, err := filepath.Glob(filepath.Join(workDir, ".solcode", "acp-uploads", "*"))
	if err != nil {
		t.Fatalf("glob uploads: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected uploaded image file")
	}
}

func TestACPSessionNewStartsFreshDespitePersistedHistory(t *testing.T) {
	workDir := t.TempDir()
	sessionDir := t.TempDir()
	store := session.NewFileStore(sessionDir)
	existing := session.NewSession("session-1", workDir, "test-model")
	existing.Append(sdk.NewUserMessage(sdk.NewTextBlock("previous question")))
	existing.Append(sdk.NewAssistantMessage(sdk.NewTextBlock("previous answer")))
	if err := store.Save(context.Background(), existing); err != nil {
		t.Fatalf("save session: %v", err)
	}

	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.WorkDir = workDir
		cfg.Session.Enabled = true
		cfg.Session.Persist = true
		cfg.Session.Dir = sessionDir
		cfg.Memory.Enabled = false
		cfg.KnowledgeGraph.Enabled = false
		return cfg
	}, nil)

	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	created := mustCall(t, client, MethodSessionNew, map[string]any{"cwd": workDir})
	var newResult SessionNewResult
	if err := json.Unmarshal(created, &newResult); err != nil {
		t.Fatalf("decode session/new: %v", err)
	}
	if !strings.HasPrefix(newResult.SessionID, "acp-") {
		t.Fatalf("session/new id = %q, want fresh acp-* id", newResult.SessionID)
	}
	if newResult.SessionID == "session-1" {
		t.Fatal("session/new reused persisted session id")
	}
	if snap := client.snapshot(); hasText(snap, "user_message_chunk", "previous question") || hasText(snap, "agent_message_chunk", "previous answer") {
		t.Fatalf("session/new replayed old history: %+v", snap)
	}
}

func TestACPMCPServerEnvironmentDecoding(t *testing.T) {
	for _, raw := range []string{
		`{"mcpServers":[{"name":"array-env","command":"node","env":[{"name":"TOKEN","value":"secret"},{"name":"DEBUG","value":"1"}]}]}`,
		`{"mcpServers":[{"name":"map-env","command":"node","env":{"TOKEN":"secret","DEBUG":"1"}}]}`,
	} {
		var params SessionNewParams
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			t.Fatalf("decode MCP server parameters: %v", err)
		}
		if len(params.MCPServers) != 1 {
			t.Fatalf("mcpServers = %d, want 1", len(params.MCPServers))
		}
		if got := params.MCPServers[0].Env["TOKEN"]; got != "secret" {
			t.Fatalf("TOKEN = %q, want secret", got)
		}
		if got := params.MCPServers[0].Env["DEBUG"]; got != "1" {
			t.Fatalf("DEBUG = %q, want 1", got)
		}
	}
}

func TestACPSlashHelpAndModelPicker(t *testing.T) {
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.Providers = []config.ProviderConfig{{
			Name: "local",
			Models: []config.ModelConfig{
				{Name: "fast", ID: "fast-id"},
				{Name: "smart", ID: "smart-id", Default: true},
			},
		}}
		cfg.Provider = "local"
		cfg.Model = "fast"
		return cfg
	}, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
		t.Fatalf("agent prompt should not run for slash commands, got %q", prompt)
		return agent.AgentResult{}, nil
	})
	client.permissionOption = "smart"

	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	created := mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()})
	var newResult SessionNewResult
	if err := json.Unmarshal(created, &newResult); err != nil {
		t.Fatalf("decode session/new: %v", err)
	}

	mustCall(t, client, MethodSessionPrompt, map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": "/help"}},
	})
	waitFor(t, func() bool {
		return hasText(client.snapshot(), "agent_message_chunk", "/model")
	})

	mustCall(t, client, MethodSessionPrompt, map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": "/model"}},
	})
	waitFor(t, func() bool {
		return hasText(client.snapshot(), "agent_message_chunk", "Switched model to smart")
	})
}

func TestACPSlashSessionsPicker(t *testing.T) {
	workDir := t.TempDir()
	sessionDir := t.TempDir()
	store := session.NewFileStore(sessionDir)
	existing := session.NewSession("alpha", workDir, "test-model")
	existing.Append(sdk.NewUserMessage(sdk.NewTextBlock("alpha question")))
	existing.Append(sdk.NewAssistantMessage(sdk.NewTextBlock("alpha answer")))
	if err := store.Save(context.Background(), existing); err != nil {
		t.Fatalf("save session: %v", err)
	}

	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.WorkDir = workDir
		cfg.Session.Enabled = true
		cfg.Session.Persist = true
		cfg.Session.Dir = sessionDir
		cfg.Memory.Enabled = false
		cfg.KnowledgeGraph.Enabled = false
		return cfg
	}, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
		t.Fatalf("agent prompt should not run for /sessions, got %q session=%q", prompt, sessionID)
		return agent.AgentResult{}, nil
	})
	client.permissionOption = "alpha"

	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1})
	created := mustCall(t, client, MethodSessionNew, map[string]any{"cwd": workDir})
	var newResult SessionNewResult
	if err := json.Unmarshal(created, &newResult); err != nil {
		t.Fatalf("decode session/new: %v", err)
	}

	mustCall(t, client, MethodSessionPrompt, map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": "/sessions"}},
	})
	waitFor(t, func() bool {
		snap := client.snapshot()
		return hasText(snap, "user_message_chunk", "alpha question") &&
			hasText(snap, "agent_message_chunk", "Switched session to alpha")
	})
}

func TestPromptToTextAndHistory(t *testing.T) {
	text := promptToText([]ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "resource_link", Name: "doc", URI: "file://doc.md"},
		{Type: "resource", Resource: json.RawMessage(`{"text":"embedded notes"}`)},
	}, t.TempDir())
	if !strings.Contains(text, "hello") || !strings.Contains(text, "doc") || !strings.Contains(text, "embedded notes") {
		t.Fatalf("promptToText = %q", text)
	}

	s := session.NewSession("s", t.TempDir(), "m")
	s.Append(sdk.NewUserMessage(sdk.NewTextBlock("ask")))
	s.Append(sdk.NewAssistantMessage(sdk.NewTextBlock("reply")))
	if got := len(sessionHistoryUpdates(s)); got != 2 {
		t.Fatalf("history updates = %d, want 2", got)
	}
}

type harnessClient struct {
	conn             *Conn
	permissionOption string
	mu               sync.Mutex
	updates          []SessionUpdateParams
}

func (c *harnessClient) snapshot() []SessionUpdateParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]SessionUpdateParams, len(c.updates))
	copy(out, c.updates)
	return out
}

func newACPHarness(t *testing.T, prompt PromptFunc) *harnessClient {
	t.Helper()
	return newACPHarnessWithConfig(t, nil, prompt)
}

func newACPHarnessWithConfig(t *testing.T, mutate func(config.Config) config.Config, prompt PromptFunc) *harnessClient {
	t.Helper()
	cfg := config.Default()
	cfg.APIKey = "test"
	cfg.WorkDir = t.TempDir()
	cfg.Session.Enabled = true
	cfg.Session.Persist = true
	cfg.Session.Dir = t.TempDir()
	cfg.Memory.Enabled = false
	cfg.KnowledgeGraph.Enabled = false
	if mutate != nil {
		cfg = mutate(cfg)
	}

	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	server := NewServer(cfg, 0, 0, "test", func(cfg config.Config, opts ...app.Option) (*app.App, config.Config, error) {
		application, err := app.New(cfg, opts...)
		return application, cfg, err
	})
	server.runPrompt = prompt

	go func() {
		_ = server.Serve(context.Background(), serverReader, serverWriter)
	}()
	t.Cleanup(func() {
		_ = clientReader.Close()
		_ = clientWriter.Close()
		_ = serverReader.Close()
		_ = serverWriter.Close()
		server.Close()
	})

	client := &harnessClient{
		conn:             NewConn(clientReader, clientWriter),
		permissionOption: PermissionAllowOnce,
	}
	go client.readLoop()
	return client
}

func (c *harnessClient) readLoop() {
	for {
		msg, err := c.conn.Read()
		if err != nil {
			return
		}
		if isResponse(msg) {
			continue
		}
		switch msg.Method {
		case MethodSessionUpdate:
			var params SessionUpdateParams
			if err := json.Unmarshal(msg.Params, &params); err == nil {
				c.mu.Lock()
				c.updates = append(c.updates, params)
				c.mu.Unlock()
			}
		case MethodSessionRequestPermission:
			option := c.permissionOption
			if option == "" {
				option = PermissionAllowOnce
			}
			_ = c.conn.Reply(msg.ID, RequestPermissionResult{
				Outcome: PermissionOutcome{Outcome: "selected", OptionID: option},
			})
		}
	}
}

func mustCall(t *testing.T, client *harnessClient, method string, params any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := client.conn.Call(ctx, method, params, &raw); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if method == MethodSessionPrompt && len(raw) == 0 {
		return raw
	}
	return raw
}

func mustNotify(t *testing.T, client *harnessClient, method string, params any) {
	t.Helper()
	if err := client.conn.Notify(method, params); err != nil {
		t.Fatalf("notify %s: %v", method, err)
	}
}

func hasKind(updates []SessionUpdateParams, kind string) bool {
	for _, update := range updates {
		if update.Update.SessionUpdate == kind {
			return true
		}
	}
	return false
}

func hasCommands(updates []SessionUpdateParams, names ...string) bool {
	remaining := make(map[string]struct{}, len(names))
	for _, name := range names {
		remaining[name] = struct{}{}
	}
	for _, update := range updates {
		if update.Update.SessionUpdate != "available_commands_update" {
			continue
		}
		for _, command := range update.Update.AvailableCommands {
			delete(remaining, command.Name)
		}
	}
	return len(remaining) == 0
}

func hasText(updates []SessionUpdateParams, kind, text string) bool {
	for _, update := range updates {
		if update.Update.SessionUpdate == kind && update.Update.Content != nil && strings.Contains(update.Update.Content.Text, text) {
			return true
		}
	}
	return false
}

func hasMode(updates []SessionUpdateParams, mode string) bool {
	for _, update := range updates {
		if update.Update.SessionUpdate == "current_mode_update" && update.Update.CurrentModeID == mode {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
