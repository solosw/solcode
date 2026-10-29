package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testTerminalRunner func(context.Context, *UseContext, string, []string) (*ContentBlock, error)

func (f testTerminalRunner) Run(ctx context.Context, uctx *UseContext, command string, args []string) (*ContentBlock, error) {
	return f(ctx, uctx, command, args)
}

func TestBashDelegatesToClientTerminal(t *testing.T) {
	for _, shell := range []ShellRunner{bashShellRunner{}, cmdShellRunner{}} {
		for _, input := range []string{
			`{"command":"echo hi && echo bye","timeout":1000}`,
			`{"command":"echo hi && echo bye","timeout":200000,"run_in_background":true}`,
		} {
			calls := 0
			uctx := &UseContext{WorkDir: t.TempDir(), MessageID: "bash-call", AgentID: "main"}
			uctx.TerminalRunner = testTerminalRunner(func(ctx context.Context, got *UseContext, command string, args []string) (*ContentBlock, error) {
				calls++
				wantCommand, wantArgs := shellCommand(shell, "echo hi && echo bye")
				if command != wantCommand || !reflect.DeepEqual(args, wantArgs) || got != uctx {
					t.Fatalf("command=%s args=%v context=%+v", command, args, got)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 201*time.Second {
					t.Fatalf("deadline=%v", deadline)
				}
				return Result("client output"), nil
			})
			result, err := NewBashToolWithShell(shell).Invoke(t.Context(), uctx, json.RawMessage(input))
			if err != nil || result.IsError || result.Text != "client output" || calls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
			}
		}
	}
}

func TestBashClientTerminalValidationAndCancellation(t *testing.T) {
	calls := 0
	uctx := &UseContext{TerminalRunner: testTerminalRunner(func(context.Context, *UseContext, string, []string) (*ContentBlock, error) {
		calls++
		return Result("unexpected"), nil
	})}
	for _, input := range []string{
		`{"command":""}`,
		`{"command":"curl example"}`,
		`{"command":12}`,
	} {
		result, err := NewBashTool().Invoke(t.Context(), uctx, json.RawMessage(input))
		if err != nil || !result.IsError {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := NewBashTool().Invoke(ctx, uctx, json.RawMessage(`{"command":"echo must-not-run"}`))
	if err != nil || !result.IsError || calls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
}

func TestBashClientTerminalErrorDoesNotRunLocally(t *testing.T) {
	uctx := &UseContext{TerminalRunner: testTerminalRunner(func(context.Context, *UseContext, string, []string) (*ContentBlock, error) {
		return nil, errors.New("client refused")
	})}
	result, err := NewBashTool().Invoke(t.Context(), uctx, json.RawMessage(`{"command":"echo must-not-run"}`))
	if result != nil || err == nil || !strings.Contains(err.Error(), "client refused") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBashLocalSandboxDoesNotDelegate(t *testing.T) {
	b := &bashTool{shell: DefaultShell()}
	b.sandboxPolicy.Enabled = true
	// A pre-cancelled context prevents local execution too; an external terminal
	// must never receive the command, regardless of its own sandbox support.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	uctx := &UseContext{WorkDir: t.TempDir(), TerminalRunner: testTerminalRunner(func(context.Context, *UseContext, string, []string) (*ContentBlock, error) {
		t.Fatal("sandbox bypassed via client terminal")
		return nil, nil
	})}
	result, err := b.Invoke(ctx, uctx, json.RawMessage(`{"command":"echo must-not-run"}`))
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
