package acp

import (
	"github.com/solosw/solcode/internal/tool"
	"testing"
)

func TestACPParentQuestionRoutesToChildWithoutChangingProtocolState(t *testing.T) {
	server, root, output := newSubagentRecorder()
	event := tool.AgentProgressEvent{AgentID: "child", RunID: "run", Kind: "started"}
	server.emitAgentProgress(root, event)
	event.Kind, event.Output = "needs_input", "Which API?"
	server.emitAgentProgress(root, event)
	updates := recordedSubagentUpdates(t, output)
	if len(updates) != 3 {
		t.Fatalf("updates = %+v", updates)
	}
	childID := updates[0].Update.SessionID
	last := updates[2]
	if last.SessionID != childID || last.Update.SessionUpdate != "status_update" || last.Update.Message != "Waiting for parent: Which API?" {
		t.Fatalf("question routed incorrectly: %+v", last)
	}
	if last.Update.State != nil {
		t.Fatal("waiting-parent introduced an unsupported ACP state")
	}
}
