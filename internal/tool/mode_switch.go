package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const ModeSwitchToolName = "ModeSwitch"

type ModeSwitchRequest struct {
	Mode string `json:"mode"`
}

type modeSwitchTool struct {
	BaseTool
	switchMode func(ctx context.Context, mode string) error
	startGoal  func(ctx context.Context, uctx *UseContext) error
}

func NewModeSwitchTool(switchMode func(ctx context.Context, mode string) error) Tool {
	return &modeSwitchTool{switchMode: switchMode}
}

// NewModeSwitchToolWithGoal configures ModeSwitch(goal) to start the goal flow
// after the permission-mode transition succeeds.
func NewModeSwitchToolWithGoal(switchMode func(ctx context.Context, mode string) error, startGoal func(context.Context, *UseContext) error) Tool {
	return &modeSwitchTool{switchMode: switchMode, startGoal: startGoal}
}

func (t *modeSwitchTool) Name() string { return ModeSwitchToolName }

func (t *modeSwitchTool) Description() string {
	return "Request a permission-mode transition. Entering plan from another mode requires user approval. Bypass, goal, and ultra are only available from plan and also require user approval. Switching to goal starts the goal.md workflow after approval. Use only when the user asks for plan/bypass/goal/ultra, or when a large design should be planned before implementation; do not switch modes unprompted for ordinary coding."
}

func (t *modeSwitchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"plan", "bypass", "goal", "ultra", "ultrl"},
				"description": "Requested permission mode; ultra enables Task/sub-agents. Legacy ultrl is accepted as an alias.",
			},
		},
		"required": []string{"mode"},
	}
}

func (t *modeSwitchTool) IsDestructive(json.RawMessage) bool { return false }

func (t *modeSwitchTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var req ModeSwitchRequest
	if err := json.Unmarshal(input, &req); err != nil {
		return ErrorResult("invalid mode switch request: " + err.Error()), nil
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "ultrl" {
		mode = "ultra"
	}
	if mode != "plan" && mode != "bypass" && mode != "goal" && mode != "ultra" {
		return ErrorResult("mode must be plan, bypass, goal, or ultra"), nil
	}
	if t.switchMode == nil {
		return nil, fmt.Errorf("mode switching is not configured")
	}
	if err := t.switchMode(ctx, mode); err != nil {
		return ErrorResult(err.Error()), nil
	}
	if mode == "goal" && t.startGoal != nil {
		if err := t.startGoal(ctx, uctx); err != nil {
			return ErrorResult(err.Error()), nil
		}
		return Result("Permission mode switched to goal; goal.md workflow started."), nil
	}
	return Result("Permission mode switched to " + mode + "."), nil
}
