package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestACPCancelPendingBashPermission(t *testing.T) {
	for _, name := range []string{tool.BashToolName, tool.BatchToolName} {
		t.Run(name, func(t *testing.T) {
			var invoked atomic.Int32
			client := newACPHarness(t, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
				if prompt == "next" {
					return agent.AgentResult{Output: "ready"}, nil
				}
				executor := engine.NewToolExecutorWithPermissions(application.Tools, nil, application.Permissions)
				uctx := &tool.UseContext{WorkDir: workDir, BatchLookup: application.Tools.Find}
				uctx.BatchDispatch = func(ctx context.Context, child string, input json.RawMessage) (*tool.ContentBlock, error) {
					return executor.Execute(ctx, engine.ToolCall{Name: child, Input: input}, engine.ToolEnv{
						UseContext:    uctx,
						OnInvokeStart: func() { invoked.Add(1) },
					}).Content, nil
				}
				input := json.RawMessage(`{"command":"printf forbidden > cancelled-marker"}`)
				if name == tool.BatchToolName {
					// Use the static batch wire format independent of configured PTC defaults.
					application.Tools.Register(tool.NewBatchTool())
					input = json.RawMessage(`{"operations":[{"tool":"Bash","input":{"command":"printf forbidden > cancelled-marker"}}]}`)
				}
				env := engine.ToolEnv{UseContext: uctx}
				if name == tool.BashToolName {
					env.OnInvokeStart = func() { invoked.Add(1) }
				}
				executor.Execute(ctx, engine.ToolCall{Name: name, Input: input}, env)
				return agent.AgentResult{}, ctx.Err()
			})
			requests := make(chan jsonrpcMessage, 4)
			client.mu.Lock()
			client.permissionRequests = requests
			client.mu.Unlock()
			mustCall(t, client, MethodInitialize, InitializeParams{})
			workDir := t.TempDir()
			var created SessionNewResult
			if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: workDir}), &created); err != nil {
				t.Fatal(err)
			}
			client.server.getSession(created.SessionID).application.Permissions.SetMode(permission.ModeAuto)
			done := startCancelTestPrompt(client, created.SessionID, "run")
			var pending jsonrpcMessage
			select {
			case pending = <-requests:
				var params RequestPermissionParams
				if err := json.Unmarshal(pending.Params, &params); err != nil || params.ToolCall.Title != tool.BashToolName {
					t.Fatalf("expected child Bash permission, got %s: %v", pending.Params, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no Bash permission request")
			}
			// Leave request_permission unanswered; cancellation must release it.
			mustNotify(t, client, MethodSessionCancel, SessionCancelParams{SessionID: created.SessionID})
			awaitCancelTestPrompt(t, done, StopReasonCancelled)
			awaitCancelTestPrompt(t, startCancelTestPrompt(client, created.SessionID, "next"), StopReasonEndTurn)
			// A response to the old request must not grant a session-wide allow.
			if err := client.conn.Reply(pending.ID, RequestPermissionResult{
				Outcome: PermissionOutcome{Outcome: "selected", OptionID: PermissionAllowAlways},
			}); err != nil {
				t.Fatal(err)
			}
			// An ordered round trip ensures Serve has consumed the late response.
			mustCall(t, client, MethodAuthenticate, nil)
			if invoked.Load() != 0 {
				t.Fatalf("cancelled Bash invoked %d times", invoked.Load())
			}
			if _, err := os.Stat(filepath.Join(workDir, "cancelled-marker")); !os.IsNotExist(err) {
				t.Fatalf("cancelled Bash produced a side effect: %v", err)
			}
			sess := client.server.getSession(created.SessionID)
			sess.mu.Lock()
			active := sess.prompting || sess.promptCtx != nil || sess.cancel != nil
			sess.mu.Unlock()
			if active {
				t.Fatal("completed prompt retained cancellation state")
			}
			// With no active prompt, approval must fail closed, without asking again.
			if sess.application.Permissions.RequestApproval(tool.BashToolName, "old approval") {
				t.Fatal("approval outside an active prompt was allowed")
			}
		})
	}
}

type cancelTestPromptResult struct {
	result SessionPromptResult
	err    error
}

func startCancelTestPrompt(client *harnessClient, sessionID, text string) <-chan cancelTestPromptResult {
	done := make(chan cancelTestPromptResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var result SessionPromptResult
		err := client.conn.Call(ctx, MethodSessionPrompt, SessionPromptParams{
			SessionID: sessionID,
			Prompt:    []ContentBlock{{Type: "text", Text: text}},
		}, &result)
		done <- cancelTestPromptResult{result: result, err: err}
	}()
	return done
}

func awaitCancelTestPrompt(t *testing.T, done <-chan cancelTestPromptResult, want string) {
	t.Helper()
	select {
	case got := <-done:
		if got.err != nil || got.result.StopReason != want {
			t.Fatalf("prompt = %+v, %v; want %s", got.result, got.err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt did not finish")
	}
}

type cancelTestWriter func([]byte) (int, error)

func (f cancelTestWriter) Write(p []byte) (int, error) { return f(p) }

func TestACPPermissionRejectsAllowAfterCancellation(t *testing.T) {
	for _, option := range []string{PermissionAllowOnce, PermissionAllowAlways} {
		t.Run(option, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			perms := permission.NewService(permission.ModeAuto)
			sess := &acpSession{id: "session", application: &app.App{Permissions: perms}}
			if !sess.beginPrompt(ctx, cancel) {
				t.Fatal("could not begin prompt")
			}
			defer sess.endPrompt()
			var conn *Conn
			conn = NewConn(strings.NewReader(""), cancelTestWriter(func(p []byte) (int, error) {
				var request jsonrpcMessage
				if err := json.Unmarshal(p, &request); err != nil {
					return 0, err
				}
				// Make cancellation and the allow response ready before Call selects.
				// Either select branch must deny the tool; no sleeps or retries.
				cancel()
				raw, _ := json.Marshal(RequestPermissionResult{Outcome: PermissionOutcome{Outcome: "selected", OptionID: option}})
				conn.dispatchResponse(jsonrpcMessage{ID: request.ID, Result: raw})
				return len(p), nil
			}))
			defer conn.Close()
			server := &Server{conn: conn}
			if server.requestPermission(sess, tool.BashToolName, "run Bash") {
				t.Fatal("cancelled approval allowed Bash")
			}
			// Test the policy without another interactive approver: allow_always
			// must not have been persisted even if Call consumed the response.
			if perms.Check(tool.NewBashTool(), json.RawMessage(`{"command":"printf forbidden > marker"}`)).Allowed {
				t.Fatal("cancelled allow_always persisted")
			}
		})
	}
}

func TestACPPreparedPromptCanCancelBeforeExecution(t *testing.T) {
	sess := &acpSession{id: "session"}
	server := &Server{
		initialized: true,
		sessions:    map[string]*acpSession{sess.id: sess},
		conn:        NewConn(strings.NewReader(""), io.Discard),
	}
	defer server.Close()
	var invoked bool
	server.runPrompt = func(ctx context.Context, _ *app.App, _, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		invoked = true
		if ctx.Err() != context.Canceled {
			t.Errorf("queued prompt context = %v; want canceled before execution", ctx.Err())
		}
		return agent.AgentResult{}, ctx.Err()
	}
	params, _ := json.Marshal(SessionPromptParams{SessionID: sess.id, Prompt: []ContentBlock{{Type: "text", Text: "run"}}})
	run, err := server.prepareSessionPrompt(context.Background(), jsonrpcMessage{ID: json.RawMessage("1"), Params: params})
	if err != nil || run == nil {
		t.Fatalf("prepare prompt: run available=%v, err=%v", run != nil, err)
	}
	// Deliberately do not schedule the runner until the cancellation has been
	// processed. This fixes the ordering without sleeps or scheduler retries.
	cancelParams, _ := json.Marshal(SessionCancelParams{SessionID: sess.id})
	if err := server.handleSessionCancel(jsonrpcMessage{Params: cancelParams}); err != nil {
		t.Fatal(err)
	}
	if invoked {
		t.Fatal("preparation executed the prompt")
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if !invoked {
		t.Fatal("prompt runner did not execute")
	}
}

type cancelTestReader func([]byte) (int, error)

func (f cancelTestReader) Read(p []byte) (int, error) { return f(p) }

func TestACPServeRegistersPromptBeforeReadingCancel(t *testing.T) {
	sess := &acpSession{id: "session"}
	server := &Server{initialized: true, sessions: map[string]*acpSession{sess.id: sess}}
	server.runPrompt = func(ctx context.Context, _ *app.App, _, _, _ string, _ int, _ StreamEmitter) (agent.AgentResult, error) {
		<-ctx.Done()
		return agent.AgentResult{}, ctx.Err()
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"session","prompt":[{"type":"text","text":"run"}]}}` + "\n",
		`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"session"}}` + "\n",
	}
	finished := make(chan struct{})
	registered := make(chan bool, 1)
	read := 0
	r := cancelTestReader(func(p []byte) (int, error) {
		if read == 1 {
			// Observe the exact read boundary, not scheduler timing or a retry.
			sess.mu.Lock()
			registered <- sess.prompting && sess.promptCtx != nil && sess.cancel != nil
			sess.mu.Unlock()
		}
		if read < len(frames) {
			n := copy(p, frames[read])
			read++
			return n, nil
		}
		<-finished
		return 0, io.EOF
	})
	response := make(chan jsonrpcMessage, 1)
	w := cancelTestWriter(func(p []byte) (int, error) {
		var msg jsonrpcMessage
		if err := json.Unmarshal(p, &msg); err != nil {
			return 0, err
		}
		response <- msg
		close(finished)
		return len(p), nil
	})
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), r, w) }()
	select {
	case ok := <-registered:
		if !ok {
			t.Error("Serve read cancellation before registering the prompt")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not read cancellation")
	}
	select {
	case msg := <-response:
		var result SessionPromptResult
		if err := json.Unmarshal(msg.Result, &result); err != nil || result.StopReason != StopReasonCancelled {
			t.Fatalf("prompt response = %+v: %v", msg, err)
		}
	case <-time.After(3 * time.Second):
		sess.cancelPrompt()
		t.Fatal("queued cancellation did not finish prompt")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not finish")
	}
}
