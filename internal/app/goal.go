package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/tool"
)

const goalFileName = "goal.md"

var goalStatusPattern = regexp.MustCompile(`(?im)^\s*GOAL_STATUS:\s*(COMPLETE|INCOMPLETE)\s*$`)

// EnsureGoalFile creates a goal checklist when it does not exist. When a goal
// already exists, a new user goal is appended instead of replacing prior work.
func EnsureGoalFile(workDir, requestedGoal string) (string, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return "", fmt.Errorf("goal work directory is required")
	}
	path := filepath.Join(workDir, goalFileName)
	requestedGoal = strings.TrimSpace(requestedGoal)

	data, err := os.ReadFile(path)
	if err == nil {
		if requestedGoal == "" || strings.Contains(string(data), requestedGoal) {
			return path, nil
		}
		appendix := fmt.Sprintf("\n\n## Additional requested goal\n\n%s\n\n## Completion checklist\n\n- [ ] Implement the additional requested goal.\n- [ ] Verify the additional requested goal.\n", requestedGoal)
		if err := os.WriteFile(path, append(data, []byte(appendix)...), 0o644); err != nil {
			return "", fmt.Errorf("update goal file: %w", err)
		}
		return path, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read goal file: %w", err)
	}
	if requestedGoal == "" {
		return "", fmt.Errorf("%s does not exist; provide a goal with /goal <description>", goalFileName)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", fmt.Errorf("create goal work directory: %w", err)
	}
	content := fmt.Sprintf("# Goal\n\n%s\n\n## Completion checklist\n\n- [ ] Understand the existing project and plan the required changes.\n- [ ] Implement the requested goal.\n- [ ] Add or update focused tests.\n- [ ] Run relevant validation and record the results.\n", requestedGoal)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("create goal file: %w", err)
	}
	return path, nil
}

// CheckGoalCompletion runs a reusable task sub-agent that keeps goal.md as
// the authoritative feature checklist, then audits the worktree. The checker
// updates checklist items only after independently verifying their evidence.
// Its final line is GOAL_STATUS: COMPLETE or GOAL_STATUS: INCOMPLETE.
func (a *App) CheckGoalCompletion(ctx context.Context, workDir string) (bool, string, error) {
	if a == nil || a.Coordinator == nil {
		return false, "", fmt.Errorf("agent coordinator is not configured")
	}
	goalPath := filepath.Join(workDir, goalFileName)
	if _, err := os.Stat(goalPath); err != nil {
		return false, "", fmt.Errorf("inspect goal file: %w", err)
	}
	prompt := fmt.Sprintf(`You are the reusable goal-checking sub-agent for this project. %q is the authoritative feature checklist.

First inspect goal.md, the relevant implementation, and applicable tests. Then update goal.md yourself so it accurately reflects the independently verified state:
- Mark a checklist item [x] only when its implementation and validation evidence are present.
- Return any incorrectly checked or unsupported item to [ ].
- Add concrete missing feature or validation items when the existing checklist is incomplete or too vague.
- Keep the file concise; do not implement application features or change files other than goal.md.

After updating goal.md, decide completion from the updated checklist. The goal is COMPLETE only when every checklist item is [x] and the requested outcome has validation evidence. If anything remains, explain the specific unchecked work.

End your response with exactly one line:
GOAL_STATUS: COMPLETE
or
GOAL_STATUS: INCOMPLETE`, goalPath)
	payload, err := json.Marshal(tool.TaskParams{
		Description:  "Check goal completion",
		Prompt:       prompt,
		AllowedTools: []string{"Glob", "Grep", "LS", "View", "Bash", "Edit", "Write"},
	})
	if err != nil {
		return false, "", fmt.Errorf("encode goal checker task: %w", err)
	}
	result, err := tool.NewTaskTool(a.Coordinator).Invoke(ctx, &tool.UseContext{
		AgentID:           "goal",
		WorkDir:           workDir,
		SubagentModel:     a.Config.Subagents.Model,
		ModelContextLimit: a.Config.ContextLimitForModel,
		FastModel:         a.Config.FastModel,
		Status:            a.onStatus,
		TaskRetryDelay:    0,
	}, payload)
	if err != nil {
		return false, "", fmt.Errorf("run goal checker task: %w", err)
	}
	if result == nil {
		return false, "", fmt.Errorf("goal checker returned no result")
	}
	report := strings.TrimSpace(result.Text)
	if result.IsError {
		return false, report, fmt.Errorf("goal checker failed: %s", report)
	}
	matches := goalStatusPattern.FindStringSubmatch(report)
	if len(matches) != 2 {
		return false, report, fmt.Errorf("goal checker did not return GOAL_STATUS")
	}
	return matches[1] == "COMPLETE", report, nil
}

// RunGoalWithSession executes a goal-directed conversation. Every main-agent
// turn is followed by CheckGoalCompletion; unfinished goals receive an
// automatic continuation turn until the checker verifies completion or the
// caller cancels the context.
func (a *App) RunGoalWithSession(ctx context.Context, sessionID, requestedGoal, workDir string, maxTurns int) (agent.AgentResult, error) {
	if a == nil {
		return agent.AgentResult{}, fmt.Errorf("app is nil")
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = a.Config.WorkDir
	}
	goalPath, err := EnsureGoalFile(workDir, requestedGoal)
	if err != nil {
		return agent.AgentResult{}, err
	}
	prompt := fmt.Sprintf("Work toward the goal defined in %s. Read it first, implement the outstanding functionality, and validate your work. The goal-checking sub-agent owns all goal.md checklist updates, so do not edit goal.md yourself. Do not stop until the goal is ready for independent verification.", goalPath)
	if requestedGoal != "" {
		prompt += "\n\nThe user requested: " + strings.TrimSpace(requestedGoal)
	}

	for {
		if err := ctx.Err(); err != nil {
			return agent.AgentResult{}, err
		}
		result, err := a.RunPromptWithSession(ctx, sessionID, prompt, workDir, maxTurns)
		if err != nil {
			return result, err
		}
		if result.Error != "" {
			return result, nil
		}
		if a.onStatus != nil {
			a.onStatus("Checking goal completion...")
		}
		complete, report, err := a.CheckGoalCompletion(ctx, workDir)
		if err != nil {
			return result, err
		}
		if complete {
			return result, nil
		}
		prompt = "The goal checker reports that the goal is not complete. Continue implementing and validating the remaining functionality. The goal-checking sub-agent owns goal.md updates, so do not edit that file yourself.\n\nChecker report:\n" + report
	}
}
