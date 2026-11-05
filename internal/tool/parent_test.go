package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/tool"
)

type parentToolRunner struct{ ask tool.Tool }

func (r *parentToolRunner) Run(context.Context, agent.AgentConfig) agent.AgentResult {
	panic("persistent runner required")
}
func (r *parentToolRunner) RunSession(ctx context.Context, req agent.SessionRequest) agent.AgentResult {
	call := sdk.NewAssistantMessage(sdk.NewToolUseBlock("ask", map[string]any{"question": "choose"}, tool.AskParentToolName))
	messages := []sdk.MessageParam{call}
	if err := req.Save(agent.Checkpoint{Snapshot: agent.Snapshot{Messages: messages}, Stage: "response"}); err != nil {
		return agent.AgentResult{Error: err.Error()}
	}
	result, err := r.ask.Invoke(ctx, &tool.UseContext{AgentID: string(req.Config.ID), AgentRole: "task", RunID: req.Config.RunID, MessageID: "ask"}, json.RawMessage(`{"question":"choose","context":"A or B"}`))
	if err != nil {
		return agent.AgentResult{Error: err.Error()}
	}
	if result.IsError {
		return agent.AgentResult{Error: result.Text}
	}
	messages = append(messages, sdk.NewUserMessage(sdk.NewToolResultBlock("ask", result.Text, false)))
	if err := req.Save(agent.Checkpoint{Snapshot: agent.Snapshot{Messages: messages}, Stage: "tool_result"}); err != nil {
		return agent.AgentResult{Error: err.Error()}
	}
	return agent.AgentResult{Output: result.Text}
}

func TestParentToolsWaitDeliveryAndIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	runner := &parentToolRunner{}
	c := agent.NewCoordinator(runner)
	defer c.Close()
	runner.ask = tool.NewAskParentTool(c)
	task := tool.NewTaskTool(c)
	reply := tool.NewReplyChildTool(c)
	scope := agent.Scope{ParentID: "main", ParentSessionID: "session", WorkDir: t.TempDir()}
	u := &tool.UseContext{AgentID: "main", SessionID: "session", WorkDir: scope.WorkDir}
	event := make(chan struct{}, 1)
	c.SetEventHandler(func(e agent.Event) {
		if e.Kind == agent.EventNeedsInput {
			event <- struct{}{}
		}
	})
	if _, err := c.Spawn(ctx, agent.AgentConfig{ID: "child", RunID: "run", ParentID: scope.ParentID, ParentSessionID: scope.ParentSessionID, WorkDir: scope.WorkDir}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-event:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	result := invokeTaskRaw(t, task, u, map[string]any{"action": "questions"})
	requireTask(t, result)
	var questions []agent.ParentRequest
	if err := json.Unmarshal([]byte(result.Text), &questions); err != nil || len(questions) != 1 {
		t.Fatalf("questions=%v %v", questions, err)
	}
	waiting := invokeTaskRaw(t, task, u, map[string]any{"action": "wait", "agent_id": "child", "run_id": "run"})
	requireTask(t, waiting)
	if !strings.Contains(waiting.Text, "waiting_parent") || !strings.Contains(waiting.Text, questions[0].ID) {
		t.Fatalf("wait failed to yield: %s", waiting.Text)
	}
	source := task.(tool.AgentNotificationSource)
	if got := source.DrainAgentNotifications(u); len(got) != 1 || !strings.Contains(got[0], questions[0].ID) {
		t.Fatalf("notification=%v", got)
	}
	if len(source.DrainAgentNotifications(u)) != 0 {
		t.Fatal("question delivered twice without a new request")
	}
	input, _ := json.Marshal(map[string]any{"agent_id": "child", "run_id": "run", "request_id": questions[0].ID, "answer": "B"})
	for _, bad := range []*tool.UseContext{{AgentID: "other", SessionID: "other", WorkDir: scope.WorkDir}, {AgentID: "main", SessionID: "session", WorkDir: scope.WorkDir, AgentRole: "task"}} {
		rejected, err := reply.Invoke(ctx, bad, input)
		if err != nil || !rejected.IsError {
			t.Fatal("foreign or child caller replied")
		}
	}
	accepted, err := reply.Invoke(ctx, u, input)
	if err != nil || accepted.IsError {
		t.Fatalf("reply=%+v %v", accepted, err)
	}
	done, err := c.Wait(ctx, "child")
	if err != nil || done.Output != "B" {
		t.Fatalf("answer=%+v %v", done, err)
	}
	rejected, err := runner.ask.Invoke(ctx, u, json.RawMessage(`{"question":"wrong caller"}`))
	if err != nil || !rejected.IsError {
		t.Fatal("main called AskParent")
	}
}
