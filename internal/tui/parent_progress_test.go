package tui

import (
	"strings"
	"testing"
)

func TestParentQuestionProgressWaitAndContinue(t *testing.T) {
	var m Model
	question := AgentProgressMsg{Kind: "needs_input", AgentID: "child", Output: "Which API?"}
	m.applyAgentProgress(question)
	if len(m.agentActivities) != 1 || m.agentActivities[0].State != "waiting_parent" {
		t.Fatalf("waiting activity = %+v", m.agentActivities)
	}
	if line := agentProgressLine(question); !strings.Contains(line, "waiting for parent: Which API?") {
		t.Fatalf("question progress = %q", line)
	}
	m.applyAgentProgress(AgentProgressMsg{Kind: "tool_done", AgentID: "child", ToolName: "AskParent", Output: "Keep the API"})
	if m.agentActivities[0].State != "running" {
		t.Fatalf("answered child still waiting: %+v", m.agentActivities[0])
	}
}
