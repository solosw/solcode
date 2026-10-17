package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/solosw/solcode/internal/agent"
)

func (t *subagentTool) applyRole(ctx context.Context, uctx *UseContext, params *SubagentParams) error {
	params.AgentType = strings.TrimSpace(params.AgentType)
	if params.resume {
		record, err := t.coordinator.Get(ctx, agent.AgentID(params.AgentID), taskScope(uctx))
		if err != nil {
			return err
		}
		if params.AgentType != "" && params.AgentType != record.Config.AgentType {
			return fmt.Errorf("agent_type cannot change on resume: %s", record.Config.AgentType)
		}
		params.AgentType = record.Config.AgentType
		if params.Description == "" {
			params.Description = record.Config.Description
		}
	} else if params.AgentType == "" {
		params.AgentType = "worker"
	}
	workDir := ""
	if uctx != nil {
		workDir = uctx.WorkDir
		if uctx.PermissionMode == "bypass" && !agent.IsBuiltinType(params.AgentType) {
			return fmt.Errorf("bypass only allows explorer, worker, reviewer; agent_type=%q", params.AgentType)
		}
		params.AllowedTools = agent.IntersectTools(uctx.AllowedTools, params.AllowedTools)
	}
	// Legacy instances without a type remain resumable in ultra, but do not
	// silently become workers (or acquire a different role) in bypass.
	if params.AgentType == "" {
		return nil
	}
	role, err := agent.LoadRole(params.AgentType, workDir)
	if err != nil {
		return err
	}
	params.roleInstructions = role.Instructions
	params.AllowedTools = agent.IntersectTools(params.AllowedTools, role.AllowedTools)
	if params.Model == "" {
		params.Model = role.Model
	}
	return nil
}

// CheckBypassAgentTypes is a coarse input gate. Resume without agent_type must
// also check the persisted type in applyRole; an id is not a role declaration.
func CheckBypassAgentTypes(input json.RawMessage) error {
	var params TaskParams
	if err := json.Unmarshal(input, &params); err != nil {
		return fmt.Errorf("invalid Task parameters: %w", err)
	}
	switch params.Action {
	case "list", "status", "history", "wait", "cancel", "archive":
		return nil
	}
	check := func(name string) error {
		name = strings.TrimSpace(name)
		if name != "" && !agent.IsBuiltinType(name) {
			return fmt.Errorf("bypass only allows explorer, worker, reviewer; agent_type=%q", name)
		}
		return nil
	}
	if err := check(params.AgentType); err != nil {
		return err
	}
	for _, task := range params.Tasks {
		if err := check(task.AgentType); err != nil {
			return err
		}
	}
	return nil
}
