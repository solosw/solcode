package acp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/tool"
)

// TerminalCreateParams follows ACP v1. Bash is passed as a program plus argv,
// never as a command string requiring a second layer of client-side shell parsing.
type TerminalCreateParams struct {
	SessionID       string   `json:"sessionId"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	Cwd             string   `json:"cwd,omitempty"`
	OutputByteLimit int      `json:"outputByteLimit,omitempty"`
}

type TerminalCreateResult struct {
	TerminalID string `json:"terminalId"`
}

type TerminalParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

type TerminalExitStatus struct {
	ExitCode *int    `json:"exitCode"`
	Signal   *string `json:"signal"`
}

type TerminalOutputResult struct {
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
	ExitStatus *TerminalExitStatus `json:"exitStatus,omitempty"`
}

const terminalRequestTimeout = 5 * time.Second

type clientTerminalRunner struct {
	server  *Server
	session *acpSession
}

var _ tool.TerminalRunner = (*clientTerminalRunner)(nil)

func (s *Server) terminalRunner(sess *acpSession, enabled bool) tool.TerminalRunner {
	if s == nil || s.conn == nil || sess == nil || !enabled {
		return nil
	}
	return &clientTerminalRunner{server: s, session: sess}
}

func (r *clientTerminalRunner) Run(ctx context.Context, uctx *tool.UseContext, command string, args []string) (result *tool.ContentBlock, err error) {
	if err := ctx.Err(); err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	cwd := r.session.workDir
	if uctx != nil && uctx.WorkDir != "" {
		cwd = uctx.WorkDir
	}
	if !filepath.IsAbs(cwd) {
		return tool.ErrorResult("ACP terminal cwd must be absolute"), nil
	}

	created, createErr := r.create(ctx, TerminalCreateParams{
		SessionID: r.session.id, Command: command, Args: args, Cwd: cwd, OutputByteLimit: tool.MaxOutputLength,
	})
	if createErr != nil {
		return tool.ErrorResult(fmt.Sprintf("%s: %v (command not retried locally)", MethodTerminalCreate, createErr)), nil
	}
	if strings.TrimSpace(created.TerminalID) == "" {
		return tool.ErrorResult("terminal/create returned an empty terminalId; command not retried locally"), nil
	}
	params := TerminalParams{SessionID: r.session.id, TerminalID: created.TerminalID}
	defer func() {
		if releaseErr := r.cleanupCall(ctx, MethodTerminalRelease, params, nil); releaseErr != nil {
			if result == nil {
				result = tool.ErrorResult("")
			}
			result.Text += fmt.Sprintf("\nterminal/release failed: %v", releaseErr)
			result.IsError = true
		}
	}()
	r.server.attachTerminal(r.session, uctx, created.TerminalID)

	var status TerminalExitStatus
	waitErr := ctx.Err()
	if waitErr == nil {
		waitErr = r.server.conn.Call(ctx, MethodTerminalWaitForExit, params, &status)
	}
	if waitErr != nil {
		// Cancellation/errors do not transfer ownership of this process to the
		// client. Kill while preserving the final output, then always release it.
		killErr := r.cleanupCall(ctx, MethodTerminalKill, params, nil)
		if killErr != nil {
			waitErr = fmt.Errorf("%w; terminal/kill failed: %v", waitErr, killErr)
		}
	}
	var output TerminalOutputResult
	outputErr := r.cleanupCall(ctx, MethodTerminalOutput, params, &output)
	if output.ExitStatus != nil {
		status = *output.ExitStatus
	}
	var executionErr error
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		executionErr = fmt.Errorf("Command timed out")
	case errors.Is(ctx.Err(), context.Canceled):
		executionErr = fmt.Errorf("Command canceled")
	case waitErr != nil:
		executionErr = fmt.Errorf("terminal/wait_for_exit: %w", waitErr)
	case status.Signal != nil && *status.Signal != "":
		executionErr = fmt.Errorf("Command terminated by signal %s", *status.Signal)
	case status.ExitCode == nil:
		executionErr = fmt.Errorf("Command exited without an exit code or signal")
	}
	if outputErr != nil {
		if executionErr == nil {
			executionErr = fmt.Errorf("terminal/output: %w", outputErr)
		} else {
			executionErr = fmt.Errorf("%w; terminal/output: %v", executionErr, outputErr)
		}
	}
	exitCode := 0
	if status.ExitCode != nil {
		exitCode = *status.ExitCode
	}
	text := output.Output
	if output.Truncated {
		text += "\n[Terminal output truncated by client]"
	}
	return commandResult(text, exitCode, executionErr), nil
}

// create lets prompt cancellation return promptly, but retains the in-flight
// request until its reply (or disconnect). A client may still be asking for
// approval; do not discard the only handle to a process it creates later.
func (r *clientTerminalRunner) create(ctx context.Context, params TerminalCreateParams) (TerminalCreateResult, error) {
	type reply struct {
		result TerminalCreateResult
		err    error
	}
	replies := make(chan reply)
	go func() {
		var result TerminalCreateResult
		err := r.server.conn.Call(context.WithoutCancel(ctx), MethodTerminalCreate, params, &result)
		select {
		case replies <- reply{result, err}:
		case <-ctx.Done():
			if err == nil && strings.TrimSpace(result.TerminalID) != "" {
				terminal := TerminalParams{SessionID: r.session.id, TerminalID: result.TerminalID}
				_ = r.cleanupCall(ctx, MethodTerminalKill, terminal, nil)
				_ = r.cleanupCall(ctx, MethodTerminalRelease, terminal, nil)
			}
		}
	}()
	select {
	case received := <-replies:
		return received.result, received.err
	case <-ctx.Done():
		return TerminalCreateResult{}, ctx.Err()
	}
}

// Cleanup requests use a bounded, uncancelled context. The main prompt's
// deadline must not prevent kill/output/release from reaching the client.
func (r *clientTerminalRunner) cleanupCall(parent context.Context, method string, params TerminalParams, result any) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), terminalRequestTimeout)
	defer cancel()
	return r.server.conn.Call(ctx, method, params, result)
}

func commandResult(output string, exitCode int, executionErr error) *tool.ContentBlock {
	output = tool.TruncateOutput(output, tool.MaxOutputLength)
	if executionErr != nil {
		if output != "" {
			output += "\n"
		}
		output += executionErr.Error()
	} else if exitCode != 0 {
		if output != "" {
			output += "\n"
		}
		output += fmt.Sprintf("Exit code %d", exitCode)
	}
	if output == "" {
		output = "no output"
	}
	return &tool.ContentBlock{Type: "text", Text: output, IsError: executionErr != nil || exitCode != 0}
}

func (s *Server) attachTerminal(root *acpSession, uctx *tool.UseContext, terminalID string) {
	if uctx == nil || uctx.MessageID == "" {
		return
	}
	owner, id := root, uctx.MessageID
	if uctx.AgentRole != "" && uctx.AgentRole != "main" {
		if root.subagentsEnabled {
			root.subagentMu.Lock()
			defer root.subagentMu.Unlock()
			child := root.subagents[uctx.AgentID]
			if child == nil || child.finished || (uctx.RunID != "" && child.runID != uctx.RunID) {
				return
			}
			owner = child.session
			if child.runID != "" {
				id = child.runID + "/" + id
			}
		} else {
			root.mu.Lock()
			id = root.agentToolCalls[uctx.AgentID]
			root.mu.Unlock()
			if id == "" {
				return
			}
		}
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.toolTerminals == nil {
		owner.toolTerminals = make(map[string][]ToolCallContent)
	}
	owner.toolTerminals[id] = append(owner.toolTerminals[id], ToolCallContent{Type: "terminal", TerminalID: terminalID})
	// Send every reference so multiple Bash calls in a BatchTool card accumulate,
	// rather than replacing each other's live terminals.
	s.emitUpdate(owner.id, SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: id, ToolContent: append([]ToolCallContent(nil), owner.toolTerminals[id]...)})
}
