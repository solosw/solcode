package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

type terminalFakeOptions struct {
	holdWait   bool
	failMethod string
	emptyID    bool
	status     TerminalExitStatus
	output     TerminalOutputResult
	onCreate   func()
}

type terminalFake struct {
	client  *harnessClient
	mu      sync.Mutex
	calls   []jsonrpcMessage
	waiting chan jsonrpcMessage
}

func startTerminalFake(t *testing.T, client *harnessClient, options terminalFakeOptions) *terminalFake {
	t.Helper()
	requests := make(chan jsonrpcMessage, 32)
	client.mu.Lock()
	client.terminalRequests = requests
	client.mu.Unlock()
	f := &terminalFake{client: client, waiting: make(chan jsonrpcMessage, 32)}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case msg := <-requests:
				f.mu.Lock()
				f.calls = append(f.calls, msg)
				n := len(f.calls)
				f.mu.Unlock()
				if msg.Method == options.failMethod {
					_ = client.conn.ReplyError(msg.ID, JSONRPCInternalError, "client refused")
					continue
				}
				switch msg.Method {
				case MethodTerminalCreate:
					if options.onCreate != nil {
						options.onCreate()
					}
					id := fmt.Sprintf("term-%d", n)
					if options.emptyID {
						id = ""
					}
					_ = client.conn.Reply(msg.ID, TerminalCreateResult{TerminalID: id})
				case MethodTerminalWaitForExit:
					if options.holdWait {
						f.waiting <- msg
					} else {
						_ = client.conn.Reply(msg.ID, options.status)
					}
				case MethodTerminalOutput:
					_ = client.conn.Reply(msg.ID, options.output)
				default:
					_ = client.conn.Reply(msg.ID, map[string]any{})
				}
			}
		}
	}()
	return f
}

func (f *terminalFake) snapshot() []jsonrpcMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jsonrpcMessage(nil), f.calls...)
}
func (f *terminalFake) methods() []string {
	var methods []string
	for _, msg := range f.snapshot() {
		methods = append(methods, msg.Method)
	}
	return methods
}
func terminalZeroStatus() TerminalExitStatus { code := 0; return TerminalExitStatus{ExitCode: &code} }

func newTerminalSession(t *testing.T) (*harnessClient, *acpSession) {
	t.Helper()
	client := newACPHarness(t, nil)
	mustCall(t, client, MethodInitialize, InitializeParams{ProtocolVersion: 1, ClientCapabilities: ClientCapabilities{Terminal: true}})
	var created SessionNewResult
	if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: t.TempDir()}), &created); err != nil {
		t.Fatal(err)
	}
	return client, client.server.getSession(created.SessionID)
}

func TestACPTerminalCapabilityGate(t *testing.T) {
	client, sess := newTerminalSession(t)
	if client.server.terminalRunner(sess, false) != nil {
		t.Fatal("terminal runner enabled without capability")
	}
	if client.server.terminalRunner(sess, true) == nil {
		t.Fatal("terminal capability ignored")
	}
}

func TestACPTerminalLifecycleAndCard(t *testing.T) {
	client, sess := newTerminalSession(t)
	fake := startTerminalFake(t, client, terminalFakeOptions{status: terminalZeroStatus(), output: TerminalOutputResult{Output: "client result"}})
	input := json.RawMessage(`{"command":"echo hi && echo bye","timeout":1000}`)
	client.server.emitToolStart(sess, "Bash", input, "bash-1")
	uctx := &tool.UseContext{WorkDir: sess.workDir, MessageID: "bash-1", AgentRole: "main", TerminalRunner: client.server.terminalRunner(sess, true)}
	result, err := tool.NewBashTool().Invoke(t.Context(), uctx, input)
	if err != nil || result == nil || result.IsError || result.Text != "client result" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	client.server.emitToolDone(sess, "Bash", result.Text, result.IsError, "bash-1")
	if want := []string{MethodTerminalCreate, MethodTerminalWaitForExit, MethodTerminalOutput, MethodTerminalRelease}; !reflect.DeepEqual(fake.methods(), want) {
		t.Fatalf("methods=%v", fake.methods())
	}
	calls := fake.snapshot()
	var create TerminalCreateParams
	if err := json.Unmarshal(calls[0].Params, &create); err != nil {
		t.Fatal(err)
	}
	if create.SessionID != sess.id || create.Cwd != sess.workDir || create.Command == "" || create.OutputByteLimit != tool.MaxOutputLength || len(create.Args) != 2 || create.Args[1] != "echo hi && echo bye" {
		t.Fatalf("create=%+v", create)
	}
	for _, call := range calls[1:] {
		var params TerminalParams
		if err := json.Unmarshal(call.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params.SessionID != sess.id || params.TerminalID != "term-1" {
			t.Fatalf("params=%+v", params)
		}
	}
	waitFor(t, func() bool {
		updates := client.snapshot()
		for _, p := range updates {
			if p.Update.ToolCallID == "bash-1" && p.Update.Status == ToolCallCompleted {
				return true
			}
		}
		return false
	})
	refs := 0
	for _, p := range client.snapshot() {
		for _, content := range p.Update.ToolContent {
			if content.Type == "terminal" {
				refs++
				if p.SessionID != sess.id || p.Update.ToolCallID != "bash-1" || content.TerminalID != "term-1" {
					t.Fatalf("terminal reference=%+v", p)
				}
			}
		}
	}
	if refs != 2 {
		t.Fatalf("terminal reference not preserved on completion: %d", refs)
	}
}

func TestACPTerminalExitAndRPCFailures(t *testing.T) {
	exitCode, signal := 7, "SIGTERM"
	for _, tc := range []struct {
		name    string
		options terminalFakeOptions
		want    string
		methods []string
	}{
		{name: "nonzero", options: terminalFakeOptions{status: TerminalExitStatus{ExitCode: &exitCode}}, want: "Exit code 7"},
		{name: "signal", options: terminalFakeOptions{status: TerminalExitStatus{Signal: &signal}}, want: "SIGTERM"},
		{name: "unknown exit", want: "without an exit code"},
		{name: "truncated", options: terminalFakeOptions{status: terminalZeroStatus(), output: TerminalOutputResult{Output: "tail", Truncated: true}}, want: "truncated"},
		{name: "create rejected", options: terminalFakeOptions{failMethod: MethodTerminalCreate}, want: "not retried locally", methods: []string{MethodTerminalCreate}},
		{name: "empty terminal id", options: terminalFakeOptions{emptyID: true}, want: "empty terminalId", methods: []string{MethodTerminalCreate}},
		{name: "wait failed", options: terminalFakeOptions{failMethod: MethodTerminalWaitForExit}, want: "client refused", methods: []string{MethodTerminalCreate, MethodTerminalWaitForExit, MethodTerminalKill, MethodTerminalOutput, MethodTerminalRelease}},
		{name: "output failed", options: terminalFakeOptions{status: terminalZeroStatus(), failMethod: MethodTerminalOutput}, want: "terminal/output"},
		{name: "release failed", options: terminalFakeOptions{status: terminalZeroStatus(), failMethod: MethodTerminalRelease}, want: "terminal/release failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, sess := newTerminalSession(t)
			fake := startTerminalFake(t, client, tc.options)
			result, err := client.server.terminalRunner(sess, true).Run(t.Context(), nil, "bash", []string{"-c", "test command"})
			if err != nil || result == nil || !strings.Contains(result.Text, tc.want) || (tc.name != "truncated" && !result.IsError) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			want := tc.methods
			if want == nil {
				want = []string{MethodTerminalCreate, MethodTerminalWaitForExit, MethodTerminalOutput, MethodTerminalRelease}
			}
			if !reflect.DeepEqual(fake.methods(), want) {
				t.Fatalf("methods=%v want=%v", fake.methods(), want)
			}
		})
	}
}

func TestACPTerminalCancellationAndDeadlineCleanup(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint("deadline=", deadline), func(t *testing.T) {
			client, sess := newTerminalSession(t)
			fake := startTerminalFake(t, client, terminalFakeOptions{holdWait: true, output: TerminalOutputResult{Output: "partial output"}})
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
			}
			defer cancel()
			done := make(chan *tool.ContentBlock, 1)
			go func() {
				result, _ := client.server.terminalRunner(sess, true).Run(ctx, nil, "bash", []string{"-c", "sleep"})
				done <- result
			}()
			select {
			case <-fake.waiting:
			case <-time.After(3 * time.Second):
				t.Fatal("wait never started")
			}
			if !deadline {
				cancel()
			}
			select {
			case result := <-done:
				reason := "canceled"
				if deadline {
					reason = "timed out"
				}
				if result == nil || !result.IsError || !strings.Contains(result.Text, "partial output") || !strings.Contains(result.Text, reason) {
					t.Fatalf("result=%+v", result)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup did not complete using an uncancelled context")
			}
			want := []string{MethodTerminalCreate, MethodTerminalWaitForExit, MethodTerminalKill, MethodTerminalOutput, MethodTerminalRelease}
			if !reflect.DeepEqual(fake.methods(), want) {
				t.Fatalf("methods=%v", fake.methods())
			}
		})
	}
}

func TestACPTerminalLateCreateReplyStillReleased(t *testing.T) {
	client, sess := newTerminalSession(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	creating, releaseCreate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(releaseCreate) }) }
	defer unblock()
	fake := startTerminalFake(t, client, terminalFakeOptions{onCreate: func() { close(creating); <-releaseCreate }})
	done := make(chan *tool.ContentBlock, 1)
	go func() {
		result, _ := client.server.terminalRunner(sess, true).Run(ctx, nil, "bash", []string{"-c", "test"})
		done <- result
	}()
	select {
	case <-creating:
	case <-time.After(3 * time.Second):
		t.Fatal("create not received")
	}
	cancel()
	select {
	case result := <-done:
		if result == nil || !result.IsError || !strings.Contains(result.Text, "canceled") {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel waited for terminal/create approval")
	}
	unblock()
	waitFor(t, func() bool {
		methods := fake.methods()
		return len(methods) > 0 && methods[len(methods)-1] == MethodTerminalRelease
	})
	want := []string{MethodTerminalCreate, MethodTerminalKill, MethodTerminalRelease}
	if !reflect.DeepEqual(fake.methods(), want) {
		t.Fatalf("methods=%v", fake.methods())
	}
}

func TestACPTerminalCardRoutingAndBatchReferences(t *testing.T) {
	for _, subagents := range []bool{false, true} {
		t.Run(fmt.Sprint("subagents=", subagents), func(t *testing.T) {
			s, root, wire := newSubagentRecorder()
			root.subagentsEnabled = subagents
			event := tool.AgentProgressEvent{Kind: "started", AgentID: "child", RunID: "run"}
			s.emitAgentProgress(root, event)
			uctx := &tool.UseContext{AgentID: "child", AgentRole: "task", RunID: "run", MessageID: "batch"}
			event.Kind, event.ToolName, event.ToolUseID = "tool_start", "BatchTool", "batch"
			s.emitAgentProgress(root, event)
			s.attachTerminal(root, uctx, "term-one")
			s.attachTerminal(root, uctx, "term-two")
			event.Kind, event.Output = "tool_done", "done"
			s.emitAgentProgress(root, event)
			event.Kind = "completed"
			s.emitAgentProgress(root, event)
			updates := recordedSubagentUpdates(t, wire)
			finalRefs := 0
			for _, p := range updates {
				if p.Update.Status != ToolCallCompleted {
					continue
				}
				for _, c := range p.Update.ToolContent {
					if c.Type == "terminal" {
						finalRefs++
						if subagents && p.SessionID == root.id {
							t.Fatalf("child terminal leaked to root: %+v", p)
						}
					}
				}
			}
			if finalRefs != 2 {
				t.Fatalf("terminal references lost on completion: %d updates=%+v", finalRefs, updates)
			}
		})
	}
}

// Real ACP prompt -> App/engine -> Bash (or a PTC child) -> client terminal.
// Also verify that feature reload/model switches retain the negotiated runner.
func TestACPTerminalPromptIntegration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		batch    bool
		reload   bool
		cancel   bool
	}{
		{name: "Bash", terminal: true},
		{name: "BatchTool", terminal: true, batch: true},
		{name: "reload and model switch", terminal: true, reload: true},
		{name: "local fallback"},
		{name: "session cancel", terminal: true, cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			expected := "local-terminal-output"
			if tc.terminal {
				expected = "client-terminal-output"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				if requests.Add(1) == 1 {
					name := "Bash"
					input := map[string]any{"command": "echo local-terminal-output", "timeout": 2000}
					if tc.batch {
						name = "BatchTool"
						input = map[string]any{"code": "const a=await tools.Bash({command:'echo local-terminal-output'});console.log(a.content);const b=await tools.Bash({command:'echo local-terminal-output'});console.log(b.content);"}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "call", "type": "message", "role": "assistant", "model": "test", "stop_reason": "tool_use", "content": []any{map[string]any{"type": "tool_use", "id": "execute-1", "name": name, "input": input}}})
					return
				}
				if !strings.Contains(string(body), expected) {
					var request struct {
						Messages json.RawMessage `json:"messages"`
					}
					_ = json.Unmarshal(body, &request)
					t.Errorf("missing expected tool result %q: %s", expected, request.Messages)
				}
				_, _ = io.WriteString(w, `{"id":"final","type":"message","role":"assistant","model":"test","stop_reason":"end_turn","content":[{"type":"text","text":"all done"}]}`)
			}))
			defer upstream.Close()
			client := newACPHarnessWithConfig(t, func(cfg config.Config) config.Config {
				cfg.BaseURL, cfg.APIFormat, cfg.Model = upstream.URL, "anthropic", "test"
				cfg.Stream, cfg.BackgroundTasks.Enabled = false, false
				cfg.Batch.PTC.Enabled = true
				disabled := false
				cfg.CodeGraph.Enabled = &disabled
				cfg.PermissionMode = permission.ModeBypass
				return cfg
			}, nil)
			fake := startTerminalFake(t, client, terminalFakeOptions{holdWait: tc.cancel, status: terminalZeroStatus(), output: TerminalOutputResult{Output: "client-terminal-output"}})
			mustCall(t, client, MethodInitialize, InitializeParams{ProtocolVersion: 1, ClientCapabilities: ClientCapabilities{Terminal: tc.terminal}})
			var created SessionNewResult
			if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: t.TempDir()}), &created); err != nil {
				t.Fatal(err)
			}
			if tc.reload {
				// session/new replies before sending bootstrap. Wait for its final
				// usage update before mutating App state directly from the test.
				waitFor(t, func() bool { return hasKind(client.snapshot(), "usage_update") })
				application := client.server.getSession(created.SessionID).application
				if err := application.ReloadFeatures(application.Config, nil); err != nil {
					t.Fatal(err)
				}
				if err := application.SwitchModel(application.Config); err != nil {
					t.Fatal(err)
				}
			}
			if tc.cancel {
				done := startCancelTestPrompt(client, created.SessionID, "execute the command")
				select {
				case <-fake.waiting:
				case <-time.After(3 * time.Second):
					t.Fatal("terminal never started waiting")
				}
				mustNotify(t, client, MethodSessionCancel, SessionCancelParams{SessionID: created.SessionID})
				awaitCancelTestPrompt(t, done, StopReasonCancelled)
				want := []string{MethodTerminalCreate, MethodTerminalWaitForExit, MethodTerminalKill, MethodTerminalOutput, MethodTerminalRelease}
				if !reflect.DeepEqual(fake.methods(), want) {
					t.Fatalf("cancel lifecycle=%v", fake.methods())
				}
				return
			}
			mustCall(t, client, MethodSessionPrompt, SessionPromptParams{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: "execute the command"}}})
			if requests.Load() != 2 {
				t.Fatalf("model requests=%d", requests.Load())
			}
			if !tc.terminal {
				if len(fake.methods()) != 0 {
					t.Fatalf("unnegotiated terminal calls: %v", fake.methods())
				}
				return
			}
			want := 1
			if tc.batch {
				want = 2
			}
			creates, releases := 0, 0
			for _, method := range fake.methods() {
				if method == MethodTerminalCreate {
					creates++
				}
				if method == MethodTerminalRelease {
					releases++
				}
			}
			if creates != want || releases != want {
				t.Fatalf("creates=%d releases=%d want=%d", creates, releases, want)
			}
			waitFor(t, func() bool {
				for _, p := range client.snapshot() {
					if p.Update.ToolCallID == "execute-1" && p.Update.Status == ToolCallCompleted {
						return true
					}
				}
				return false
			})
			finalRefs := 0
			for _, p := range client.snapshot() {
				if p.Update.ToolCallID != "execute-1" || p.Update.Status != ToolCallCompleted {
					continue
				}
				for _, c := range p.Update.ToolContent {
					if c.Type == "terminal" {
						finalRefs++
					}
				}
			}
			if finalRefs != want {
				t.Fatalf("terminal references=%d want=%d", finalRefs, want)
			}
		})
	}
}
