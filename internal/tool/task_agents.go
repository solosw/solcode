package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/agent"
)

func agentReuseSchema(properties map[string]any) map[string]any {
	properties["agent_type"] = map[string]any{"type": "string", "description": "Role at creation: explorer (read-only), worker (implementation, default), reviewer (read-only review), or a configured custom role in ultra. Immutable per agent_id; omit on resume."}
	properties["agent_id"] = map[string]any{"type": "string", "description": "Stable child agent id to resume; omit to create. Never use task_id here."}
	properties["fork_context"] = map[string]any{"type": "string", "enum": []string{"none", "last_n", "full", "working_set"}, "description": "Context inheritance at creation only. Default none; parent tool outputs are not copied by default."}
	properties["fork_last_n"] = map[string]any{"type": "integer", "minimum": 1, "description": "Recent user turns to inherit for last_n (default 10), including complete tool exchanges."}
	properties["acknowledge_interruption"] = map[string]any{"type": "boolean", "description": "Resume after reconciling tool calls with unknown outcomes. First inspect history and verify effects; old calls are never replayed."}
	return properties
}

func taskResultText(result taskRunResult) string {
	if result.AgentID == "" {
		return result.Output
	}
	output := []rune(result.Output)
	if len(output) > 12_000 {
		result.Output = string(output[:12_000]) + "\n[Result shortened; use Task history for the full child transcript.]"
	}
	return fmt.Sprintf("agent_id: %s\nrun_id: %s\n\n%s", result.AgentID, result.RunID, result.Output)
}

func historyPage[T any](params TaskParams, id agent.AgentID, kind string, items []T) (*ContentBlock, error) {
	start := params.Offset
	if start > len(items) {
		start = len(items)
	}
	limit := params.Limit
	if limit == 0 {
		limit = 20
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	data, err := json.Marshal(map[string]any{"agent_id": id, "history_kind": kind, kind: items[start:end], "offset": start, "next_offset": end, "total_items": len(items)})
	if err != nil {
		return nil, err
	}
	return Result(string(data)), nil
}

func taskScope(uctx *UseContext) agent.Scope {
	if uctx == nil {
		return agent.Scope{}
	}
	return agent.Scope{ParentID: agent.AgentID(uctx.AgentID), ParentSessionID: uctx.SessionID, WorkDir: uctx.WorkDir}
}

func (t *taskTool) manage(ctx context.Context, uctx *UseContext, params TaskParams) (*ContentBlock, error) {
	if t.coordinator == nil {
		return ErrorResult("subagent coordinator is not configured"), nil
	}
	scope := taskScope(uctx)
	encode := func(value any) (*ContentBlock, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return Result(string(data)), nil
	}
	if params.Action == "list" {
		records, err := t.coordinator.Records(ctx, scope)
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		statuses := make([]agent.AgentStatus, 0, len(records))
		for _, r := range records {
			statuses = append(statuses, r.Status)
		}
		return encode(statuses)
	}
	if params.AgentID == "" {
		return ErrorResult("agent_id is required for " + params.Action), nil
	}
	id := agent.AgentID(params.AgentID)
	record, err := t.coordinator.Get(ctx, id, scope)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	switch params.Action {
	case "status":
		var usage agent.TokenUsage
		for _, run := range record.Runs {
			usage.Add(run.Usage)
		}
		return encode(map[string]any{"status": record.Status, "archived": record.Archived, "run_count": len(record.Runs), "usage": usage, "result": record.Result})
	case "history":
		if params.Offset < 0 || params.Limit < 0 || params.Limit > 100 {
			return ErrorResult("history offset must be >=0 and limit must be <=100"), nil
		}
		if params.HistoryKind == "runs" {
			return historyPage(params, id, "runs", record.Runs)
		}
		if params.HistoryKind == "events" {
			return historyPage(params, id, "events", record.Events)
		}
		if params.HistoryKind != "" && params.HistoryKind != "messages" {
			return ErrorResult("unknown history_kind"), nil
		}
		history, err := t.coordinator.History(ctx, id, scope)
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		start := params.Offset
		if start > len(history.Messages) {
			start = len(history.Messages)
		}
		limit := params.Limit
		if limit == 0 {
			limit = 20
		}
		end := start + limit
		if end > len(history.Messages) {
			end = len(history.Messages)
		}
		return encode(map[string]any{"agent_id": id, "messages": history.Messages[start:end], "offset": start, "next_offset": end, "total_messages": len(history.Messages)})
	case "wait":
		if record.Status.State == agent.AgentInterrupted && (strings.TrimSpace(params.RunID) == "" || strings.TrimSpace(params.RunID) == record.Status.RunID) {
			return ErrorResult("run was interrupted; inspect history before resuming"), nil
		}
		if params.TimeoutMS < 0 || params.TimeoutMS > 300_000 {
			return ErrorResult("wait timeout_ms must be between 1 and 300000 (default 30000)"), nil
		}
		runID := strings.TrimSpace(params.RunID)
		if runID == "" {
			runID = record.Status.RunID
		}
		timeoutMS := params.TimeoutMS
		if timeoutMS == 0 {
			timeoutMS = 30_000
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
		defer cancel()
		result, err := t.coordinator.WaitRun(waitCtx, id, runID)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return encode(map[string]any{"agent_id": id, "run_id": runID, "timed_out": true, "message": "Wait timed out; the agent was not cancelled. Continue independent work or wait again when needed."})
			}
			return ErrorResult(err.Error()), nil
		}
		return encode(result)
	case "cancel":
		if record.Status.State != agent.AgentRunning && record.Status.State != agent.AgentWaitingPermission && record.Status.State != agent.AgentCancelled {
			return Result("agent is not running: " + params.AgentID), nil
		}
		runID := strings.TrimSpace(params.RunID)
		if runID == "" {
			runID = record.Status.RunID
		}
		err = t.coordinator.CancelRun(id, runID)
	case "archive":
		err = t.coordinator.Archive(ctx, id, scope)
	default:
		return ErrorResult("unknown Task action: " + params.Action), nil
	}
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	return Result(params.Action + " requested for " + params.AgentID), nil
}
