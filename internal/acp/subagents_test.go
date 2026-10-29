package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

func TestSubagentCapabilityNegotiation(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"", false}, {"null", false}, {"true", false}, {"false", false},
		{"[]", false}, {"[{}]", false}, {"1", false}, {"\"yes\"", false},
		{"{}", true}, {"{\"future\":true}", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := supportsSubagents(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSubagentUpdateJSONRoundTrip(t *testing.T) {
	for _, wire := range []string{
		`{"sessionUpdate":"subagent_update","sessionId":"child","title":"Explorer","description":"Find files","capabilities":{}}`,
		`{"sessionUpdate":"subagent_update","sessionId":"child","state":{"state":"running"}}`,
		`{"sessionUpdate":"subagent_update","sessionId":"child","state":{"state":"idle","stopReason":"error","error":{"code":-32603,"message":"failed"}}}`,
	} {
		var update SessionUpdate
		if err := json.Unmarshal([]byte(wire), &update); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(update)
		if err != nil {
			t.Fatal(err)
		}
		var wantValue, gotValue any
		_ = json.Unmarshal([]byte(wire), &wantValue)
		_ = json.Unmarshal(got, &gotValue)
		if !reflect.DeepEqual(wantValue, gotValue) {
			t.Fatalf("round trip = %s, want %s", got, wire)
		}
	}
}

func newSubagentRecorder() (*Server, *acpSession, *bytes.Buffer) {
	var output bytes.Buffer
	server := &Server{conn: NewConn(strings.NewReader(""), &output)}
	root := &acpSession{id: "root", subagentsEnabled: true, toolCalls: make(map[string]string)}
	return server, root, &output
}

func recordedSubagentUpdates(t *testing.T, output *bytes.Buffer) []SessionUpdateParams {
	t.Helper()
	var updates []SessionUpdateParams
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	for {
		var msg jsonrpcMessage
		if err := decoder.Decode(&msg); err == io.EOF {
			return updates
		} else if err != nil {
			t.Fatal(err)
		}
		if msg.Method != MethodSessionUpdate {
			t.Fatalf("unexpected method %s", msg.Method)
		}
		var params SessionUpdateParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			t.Fatal(err)
		}
		updates = append(updates, params)
	}
}

func TestACPSubagentRoutingAndTools(t *testing.T) {
	s, root, output := newSubagentRecorder()
	event := tool.AgentProgressEvent{AgentID: "agent-a", RunID: "run-a", Description: "Explore", ParentToolUseID: "delegate"}
	send := func(kind, output string) { event.Kind, event.Output = kind, output; s.emitAgentProgress(root, event) }
	send("started", "")
	send("thinking_delta", "thinking")
	send("text_delta", "child answer")
	// Same-name, overlapping tools must be paired by their model tool-use IDs.
	event.ToolName, event.ToolInput, event.ToolUseID = "View", `{"path":"one.go"}`, "one"
	send("tool_start", "")
	event.ToolUseID = "two"
	send("tool_start", "")
	event.ToolUseID = "one"
	send("tool_done", "one result")
	event.ToolUseID, event.IsError = "two", true
	send("tool_done", "two failed")
	event.IsError = false
	send("completed", "child answer")
	updates := recordedSubagentUpdates(t, output)
	if len(updates) != 9 {
		t.Fatalf("updates (%d): %+v", len(updates), updates)
	}
	announcement := updates[0]
	childID := announcement.Update.SessionID
	if announcement.SessionID != root.id || childID == "" || childID == root.id || announcement.Update.Capabilities == nil || announcement.Update.Title != "Explore" {
		t.Fatalf("announcement = %+v", announcement)
	}
	textCount := 0
	for i, params := range updates {
		u := params.Update
		if u.SessionUpdate == "subagent_update" {
			if params.SessionID != root.id || u.SessionID != childID {
				t.Fatalf("wrong parent/child: %+v", params)
			}
		} else if params.SessionID != childID {
			t.Fatalf("child event leaked to parent: %+v", params)
		}
		if u.SessionUpdate == "agent_message_chunk" {
			textCount++
		}
		if i == 1 && (u.State == nil || u.State.State != "running") {
			t.Fatalf("missing running: %+v", u)
		}
	}
	if textCount != 1 {
		t.Fatalf("duplicated final text: %d", textCount)
	}
	if updates[6].Update.ToolCallID != "run-a/one" || updates[7].Update.ToolCallID != "run-a/two" || updates[7].Update.Status != ToolCallFailed {
		t.Fatalf("tool pairing: %+v", updates)
	}
	last := updates[len(updates)-1].Update.State
	if last == nil || last.State != "idle" || last.StopReason != StopReasonEndTurn {
		t.Fatalf("terminal state = %+v", last)
	}
}

func TestACPSubagentResumeAndLateEvents(t *testing.T) {
	s, root, output := newSubagentRecorder()
	send := func(kind, run, text string) {
		s.emitAgentProgress(root, tool.AgentProgressEvent{Kind: kind, AgentID: "reusable", RunID: run, Output: text})
	}
	send("started", "run-1", "")
	// A requested (but rejected) resume must not supersede run-1.
	before := output.Len()
	s.emitAgentProgress(root, tool.AgentProgressEvent{Kind: "started", AgentID: "reusable", RunID: "rejected", Pending: true})
	s.emitAgentProgress(root, tool.AgentProgressEvent{Kind: "failed", AgentID: "reusable", RunID: "rejected", Output: "busy"})
	if output.Len() != before {
		t.Fatal("rejected start altered the child session")
	}
	send("completed", "run-1", "first answer")
	send("retry", "run-1", "retrying")
	send("started", "run-2", "")
	before = output.Len()
	send("completed", "run-1", "stale answer")
	send("started", "run-1", "")
	send("text_delta", "run-1", "stale text")
	if output.Len() != before {
		t.Fatal("late events altered the new run")
	}
	send("failed", "run-2", "model failed")
	before = output.Len()
	send("failed", "run-2", "duplicate")
	if output.Len() != before {
		t.Fatal("duplicate terminal event was not ignored")
	}
	send("started", "run-3", "")
	send("cancelled", "run-3", "cancelled")
	updates := recordedSubagentUpdates(t, output)
	announcements, states := 0, []string{}
	retries := 0
	for _, p := range updates {
		if p.Update.SessionUpdate == "status_update" && p.Update.Message == "retrying" {
			retries++
		}
		if p.Update.Capabilities != nil {
			announcements++
		}
		if state := p.Update.State; state != nil {
			states = append(states, state.State+":"+state.StopReason)
			if state.StopReason == "error" && (state.Error == nil || state.Error.Message != "model failed") {
				t.Fatalf("missing error: %+v", state)
			}
		}
	}
	if retries != 1 {
		t.Fatalf("retry status lost between runs: %+v", updates)
	}
	if announcements != 1 || !reflect.DeepEqual(states, []string{"running:", "idle:end_turn", "running:", "idle:error", "running:", "idle:cancelled"}) {
		t.Fatalf("announcements=%d states=%v", announcements, states)
	}
}

func TestACPSubagentConcurrentIsolation(t *testing.T) {
	s, root, output := newSubagentRecorder()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("agent-%d", i)
			for _, kind := range []string{"started", "text_delta", "completed"} {
				s.emitAgentProgress(root, tool.AgentProgressEvent{Kind: kind, AgentID: id, RunID: id, Description: id, Output: id})
			}
		}(i)
	}
	wg.Wait()
	announced := map[string]string{}
	for _, p := range recordedSubagentUpdates(t, output) {
		if p.Update.Capabilities != nil {
			announced[p.Update.SessionID] = p.Update.Title
		}
		if p.SessionID != root.id {
			title, ok := announced[p.SessionID]
			if !ok {
				t.Fatalf("content before announcement: %+v", p)
			}
			if p.Update.Content != nil && p.Update.Content.Text != title {
				t.Fatalf("cross-agent text: %+v", p)
			}
		}
	}
	if len(announced) != 16 {
		t.Fatalf("announced %d children", len(announced))
	}
}

func TestACPSubagentKnownParentRouting(t *testing.T) {
	s, root, output := newSubagentRecorder()
	s.emitAgentProgress(root, tool.AgentProgressEvent{Kind: "started", AgentID: "parent"})
	s.emitAgentProgress(root, tool.AgentProgressEvent{Kind: "started", AgentID: "child", ParentAgentID: "parent"})
	updates := recordedSubagentUpdates(t, output)
	if updates[2].SessionID != updates[0].Update.SessionID {
		t.Fatalf("nested child not announced on parent: %+v", updates)
	}
}

func TestACPSubagentLegacyFallback(t *testing.T) {
	s, root, output := newSubagentRecorder()
	root.subagentsEnabled = false
	s.emitAgentProgress(root, tool.AgentProgressEvent{Kind: "started", AgentID: "child", Pending: true, Description: "Explore"})
	for _, kind := range []string{"started", "text_delta", "thinking_delta", "completed"} {
		s.emitAgentProgress(root, tool.AgentProgressEvent{Kind: kind, AgentID: "child", Description: "Explore", Output: "private delta"})
	}
	updates := recordedSubagentUpdates(t, output)
	if len(updates) != 3 || updates[0].Update.SessionUpdate != "tool_call" || updates[2].Update.Status != ToolCallCompleted {
		t.Fatalf("legacy updates = %+v", updates)
	}
	for _, p := range updates {
		if p.SessionID != root.id || p.Update.SessionUpdate == "subagent_update" || p.Update.Content != nil {
			t.Fatalf("legacy routing changed: %+v", p)
		}
	}
}

func TestACPSubagentNegotiatedPrompt(t *testing.T) {
	client := newACPHarness(t, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
		emit.AgentProgress(tool.AgentProgressEvent{Kind: "started", AgentID: "child", RunID: "run"})
		emit.AgentProgress(tool.AgentProgressEvent{Kind: "completed", AgentID: "child", RunID: "run", Output: "result"})
		return agent.AgentResult{}, nil
	})
	raw := mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"subagents": map[string]any{}}})
	var initialized InitializeResult
	if err := json.Unmarshal(raw, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.ProtocolVersion != 1 || initialized.AgentCapabilities.SessionCapabilities.Subagents == nil {
		t.Fatalf("initialize = %s", raw)
	}
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	mustCall(t, client, MethodSessionPrompt, map[string]any{"sessionId": created.SessionID, "prompt": []map[string]string{{"type": "text", "text": "delegate"}}})
	waitFor(t, func() bool { return hasText(client.snapshot(), "agent_message_chunk", "result") })
	for _, p := range client.snapshot() {
		if p.Update.Content != nil && p.Update.Content.Text == "result" && p.SessionID == created.SessionID {
			t.Fatal("child text leaked into the root session")
		}
	}
}

// Exercise the real Task -> coordinator -> engine -> App callbacks, including
// a detached completion after the root prompt has already returned.
func TestACPSubagentDetachedLifecycle(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"child-msg","type":"message","role":"assistant","model":"test","stop_reason":"end_turn","content":[{"type":"text","text":"detached answer"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
		cfg.BaseURL, cfg.APIFormat, cfg.Model = upstream.URL, "anthropic", "test"
		cfg.Stream = false
		disabled := false
		cfg.CodeGraph.Enabled = &disabled
		return cfg
	}, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
		result, err := application.Tools.Find("Task").Invoke(ctx, &tool.UseContext{
			AgentID: "main", AgentRole: "main", SessionID: sessionID, WorkDir: workDir,
			MessageID: "delegate", OnAgentProgress: emit.AgentProgress,
		}, json.RawMessage(`{"action":"spawn","description":"Detached explorer","prompt":"inspect","agent_type":"explorer"}`))
		if err != nil {
			return agent.AgentResult{}, err
		}
		if result == nil || result.IsError {
			return agent.AgentResult{}, fmt.Errorf("Task failed: %+v", result)
		}
		return agent.AgentResult{}, nil
	})
	mustCall(t, client, MethodInitialize, map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"subagents": map[string]any{}}})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, map[string]any{"cwd": t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	mustCall(t, client, MethodSessionPrompt, map[string]any{"sessionId": created.SessionID, "prompt": []map[string]string{{"type": "text", "text": "delegate"}}})
	unblock()
	waitFor(t, func() bool {
		for _, p := range client.snapshot() {
			if state := p.Update.State; state != nil && state.State == "idle" && state.StopReason == StopReasonEndTurn {
				return true
			}
		}
		return false
	})
	childID, textCount, announcements := "", 0, 0
	for _, p := range client.snapshot() {
		if p.Update.Capabilities != nil {
			childID = p.Update.SessionID
			announcements++
		}
		if p.Update.Content != nil && p.Update.Content.Text == "detached answer" {
			textCount++
			if childID == "" || p.SessionID != childID || p.SessionID == created.SessionID {
				t.Fatalf("bad detached routing: %+v", p)
			}
		}
	}
	if announcements != 1 || textCount != 1 {
		t.Fatalf("announcements=%d textCount=%d", announcements, textCount)
	}
}
