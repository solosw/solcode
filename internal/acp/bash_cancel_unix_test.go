//go:build !windows

package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
)

func TestACPCancelRunningBashAfterShellExit(t *testing.T) {
	for _, name := range []string{tool.BashToolName, tool.BatchToolName} {
		t.Run(name, func(t *testing.T) {
			client := newACPHarness(t, func(ctx context.Context, application *app.App, sessionID, prompt, workDir string, maxTurns int, emit StreamEmitter) (agent.AgentResult, error) {
				if prompt == "next" {
					return agent.AgentResult{Output: "ready"}, nil
				}
				registry := tool.NewRegistry()
				registry.Register(tool.NewBashTool(), tool.NewBatchToolWithPTC(tool.DefaultBatchLimits, true), tool.NewWriteTool())
				executor := engine.NewToolExecutorWithPermissions(registry, nil, application.Permissions)
				uctx := &tool.UseContext{WorkDir: workDir, BatchLookup: registry.Find}
				uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*tool.ContentBlock, error) {
					return executor.Execute(ctx, engine.ToolCall{Name: name, Input: input}, engine.ToolEnv{UseContext: uctx}).Content, nil
				}
				input := json.RawMessage(`{"command":"sleep 5 & echo ready > ready"}`)
				if name == tool.BatchToolName {
					input = json.RawMessage(`{"code":"await tools.Bash({command:'sleep 5 & echo ready > ready'}); await tools.Write({path:'must-not-run',content:'x'});"}`)
				}
				emit.ToolStart(name, input, "bash-cancel")
				result := executor.Execute(ctx, engine.ToolCall{Name: name, Input: input}, engine.ToolEnv{UseContext: uctx})
				emit.ToolDone(name, result.Content.Text, result.IsError, "bash-cancel")
				return agent.AgentResult{}, ctx.Err()
			})
			mustCall(t, client, MethodInitialize, InitializeParams{})
			workDir := t.TempDir()
			var created SessionNewResult
			if err := json.Unmarshal(mustCall(t, client, MethodSessionNew, SessionNewParams{Cwd: workDir}), &created); err != nil {
				t.Fatal(err)
			}
			client.server.getSession(created.SessionID).application.Permissions.SetMode(permission.ModeBypass)
			done := startCancelTestPrompt(client, created.SessionID, "run")
			waitFor(t, func() bool { _, err := os.Stat(filepath.Join(workDir, "ready")); return err == nil })
			time.Sleep(100 * time.Millisecond)
			mustNotify(t, client, MethodSessionCancel, SessionCancelParams{SessionID: created.SessionID})
			awaitCancelTestPrompt(t, done, StopReasonCancelled)
			awaitCancelTestPrompt(t, startCancelTestPrompt(client, created.SessionID, "next"), StopReasonEndTurn)
			if _, err := os.Stat(filepath.Join(workDir, "must-not-run")); !os.IsNotExist(err) {
				t.Fatal("dependent PTC mutation ran after session cancellation")
			}
		})
	}
}
