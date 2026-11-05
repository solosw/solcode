package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/tokenest"
)

type parallelForkRunner struct{}

func (parallelForkRunner) Run(context.Context, AgentConfig) AgentResult {
	return AgentResult{Error: "expected persistent runner"}
}
func (parallelForkRunner) RunSession(_ context.Context, req SessionRequest) AgentResult {
	messages := append([]sdk.MessageParam(nil), req.Snapshot.Messages...)
	messages = append(messages, sdk.NewUserMessage(sdk.NewTextBlock(req.Config.Prompt)))
	if err := req.Save(Checkpoint{Snapshot: Snapshot{Messages: messages, WorkingSet: req.Snapshot.WorkingSet}, Stage: "own-prompt"}); err != nil {
		return AgentResult{Error: err.Error()}
	}
	return AgentResult{Output: "done"}
}

func TestParallelDefaultForksHaveIndependentWindowsAndHistory(t *testing.T) {
	parent := Snapshot{}
	for i := 0; i < 200; i++ {
		parent.Messages = append(parent.Messages, sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("shared parent ", 40))))
	}
	before := tokenest.Transcript(parent.Messages)
	coordinator := NewCoordinator(parallelForkRunner{})
	defer coordinator.Close()
	workDir := t.TempDir()
	const count = 8
	for i := 0; i < count; i++ {
		cfg := AgentConfig{ID: AgentID(fmt.Sprintf("child-%d", i)), ParentID: "main", ParentSessionID: "session", Role: AgentRoleTask, WorkDir: workDir, Prompt: fmt.Sprintf("PRIVATE_CHILD_%d", i), ParentContext: parent, MaxContextTokens: int64(2000 + i*1000)}
		if _, err := coordinator.Spawn(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	scope := Scope{ParentID: "main", ParentSessionID: "session", WorkDir: workDir}
	for i := 0; i < count; i++ {
		id := AgentID(fmt.Sprintf("child-%d", i))
		result, err := coordinator.Wait(t.Context(), id)
		if err != nil || result.Error != "" {
			t.Fatalf("wait: %+v %v", result, err)
		}
		history, err := coordinator.History(t.Context(), id, scope)
		if err != nil {
			t.Fatal(err)
		}
		transcript := tokenest.Transcript(history.Messages)
		for other := 0; other < count; other++ {
			present := strings.Contains(transcript, fmt.Sprintf("PRIVATE_CHILD_%d", other))
			if present != (other == i) {
				t.Errorf("child %d history mixed with child %d", i, other)
			}
		}
		if tokens := tokenest.Messages(history.Messages); tokens > (2000+i*1000)*2/5+30 {
			t.Fatalf("child %d capacity was not isolated: %d", i, tokens)
		}
	}
	if tokenest.Transcript(parent.Messages) != before {
		t.Fatal("parallel children mutated parent history")
	}
}
