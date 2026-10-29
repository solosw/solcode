package tool_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/tool"
)

type cancelledProgressRunner struct{ cancel context.CancelFunc }

func (r cancelledProgressRunner) Run(ctx context.Context, cfg agent.AgentConfig) agent.AgentResult {
	r.cancel()
	<-ctx.Done()
	return agent.AgentResult{Error: ctx.Err().Error()}
}

func TestSubagentCancellationProgressRetainsRunIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	coordinator := agent.NewCoordinator(cancelledProgressRunner{cancel: cancel})
	subagent := tool.NewSubagentTool(coordinator)
	var events []tool.AgentProgressEvent
	result, err := subagent.Invoke(ctx, &tool.UseContext{
		AgentID: "main", SessionID: "session", WorkDir: t.TempDir(),
		OnAgentProgress: func(event tool.AgentProgressEvent) { events = append(events, event) },
	}, json.RawMessage(`{"description":"cancel me","prompt":"work"}`))
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(events) != 2 || events[0].Kind != "started" || events[1].Kind != "cancelled" {
		t.Fatalf("events=%+v", events)
	}
	if events[0].AgentID == "" || events[0].RunID == "" || events[1].AgentID != events[0].AgentID || events[1].RunID != events[0].RunID {
		t.Fatalf("run identity lost: %+v", events)
	}
	// Wait for the cancelled worker to finish before the test's temp dirs close.
	if _, err := coordinator.Wait(t.Context(), agent.AgentID(events[0].AgentID)); err != nil {
		t.Fatal(err)
	}
}
