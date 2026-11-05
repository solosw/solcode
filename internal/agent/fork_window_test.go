package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/tokenest"
)

func forkTokens(s Snapshot) int {
	return tokenest.Messages(s.Messages) + tokenest.Text(string(s.WorkingSet))
}

func TestForkWindowThresholdAndDefault(t *testing.T) {
	parent := Snapshot{WorkingSet: []byte(`{"Goal":"inherit"}`)}
	for i := 0; i < 40; i++ {
		parent.Messages = append(parent.Messages, sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("context detail ", 50))), sdk.NewAssistantMessage(sdk.NewTextBlock("answer")))
	}
	before, _ := json.Marshal(parent)
	tokens := forkTokens(parent)
	// At or just below 80%, do not reduce the full parent snapshot.
	capacity := int64((tokens*5 + 3) / 4)
	for _, mode := range []string{"", "full"} {
		out, err := selectFork(AgentConfig{ForkContext: mode, ParentContext: parent, MaxContextTokens: capacity})
		if err != nil || len(out.Messages) != len(parent.Messages) {
			t.Fatalf("below threshold: %d messages, %v", len(out.Messages), err)
		}
		out, err = selectFork(AgentConfig{ForkContext: mode, ParentContext: parent, MaxContextTokens: capacity - 2})
		if err != nil || forkTokens(out) > int((capacity-2)*2/5) || len(out.Messages) >= len(parent.Messages) {
			t.Fatalf("window: tokens=%d messages=%d err=%v", forkTokens(out), len(out.Messages), err)
		}
		if forkTokens(out) < int((capacity-2)*3/10) {
			t.Fatalf("window unexpectedly small: %d", forkTokens(out))
		}
		if tokenest.Transcript(out.Messages[len(out.Messages)-1:]) != tokenest.Transcript(parent.Messages[len(parent.Messages)-1:]) {
			t.Fatal("lost most recent context")
		}
	}
	after, _ := json.Marshal(parent)
	if !bytes.Equal(before, after) {
		t.Fatal("window mutated the parent snapshot")
	}
	for _, mode := range []string{"none", "working_set"} {
		out, err := selectFork(AgentConfig{ForkContext: mode, ParentContext: parent, MaxContextTokens: 100})
		if err != nil || len(out.Messages) != 0 {
			t.Fatalf("explicit %s ignored", mode)
		}
	}
}

func TestForkWindowToolExchangesStayPaired(t *testing.T) {
	parent := Snapshot{}
	for i := 0; i < 30; i++ {
		id := strings.Repeat("x", i+1)
		parent.Messages = append(parent.Messages,
			sdk.NewUserMessage(sdk.NewTextBlock("inspect")),
			sdk.NewAssistantMessage(sdk.NewToolUseBlock(id, map[string]any{"path": "a.go"}, "View")),
			sdk.NewUserMessage(sdk.NewToolResultBlock(id, strings.Repeat("file contents ", 80), false)))
	}
	capacity := int64(forkTokens(parent))
	out := windowFork(parent, capacity)
	if len(out.Messages) == 0 || forkTokens(out) > int(capacity*2/5) {
		t.Fatalf("bad window: %d tokens", forkTokens(out))
	}
	if len(PendingToolIDs(out.Messages)) != 0 {
		t.Fatal("orphaned tool use")
	}
	calls := map[string]bool{}
	for _, m := range out.Messages {
		for _, b := range m.Content {
			if b.OfToolUse != nil {
				calls[b.OfToolUse.ID] = true
			}
			if b.OfToolResult != nil && !calls[b.OfToolResult.ToolUseID] {
				t.Fatal("orphaned tool result")
			}
		}
	}
}

func TestForkWindowOversizedTextAndMetadata(t *testing.T) {
	parent := Snapshot{Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("很长的上下文 🧪 ", 3000) + "LATEST_MARKER"))}, WorkingSet: []byte(`{"Fact":"` + strings.Repeat("metadata ", 3000) + `"}`)}
	before, _ := json.Marshal(parent)
	out := windowFork(parent, 2000)
	if forkTokens(out) > 800 || len(out.Messages) != 1 {
		t.Fatalf("bad text window: %d", forkTokens(out))
	}
	if !strings.Contains(tokenest.Transcript(out.Messages), "LATEST_MARKER") || !utf8.ValidString(tokenest.Transcript(out.Messages)) {
		t.Fatal("tail corrupted or lost")
	}
	if len(out.WorkingSet) != 0 {
		t.Fatal("oversized metadata must be dropped whole")
	}
	after, _ := json.Marshal(parent)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated parent")
	}
	// Structured tool exchanges cannot be clipped to fit a text budget.
	out = windowFork(Snapshot{Messages: []sdk.MessageParam{sdk.NewAssistantMessage(sdk.NewToolUseBlock("large", map[string]any{}, "View")), sdk.NewUserMessage(sdk.NewToolResultBlock("large", strings.Repeat("payload ", 5000), false))}}, 2000)
	if len(out.Messages) != 0 {
		t.Fatal("oversized structured exchange was split or exceeded budget")
	}
}

func TestForkLastNAlsoHonorsChildCapacity(t *testing.T) {
	parent := Snapshot{Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("old")), sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("recent ", 5000)))}}
	out, err := selectFork(AgentConfig{ForkContext: "last_n", ForkLastN: 1, ParentContext: parent, MaxContextTokens: 2000})
	if err != nil || forkTokens(out) > 800 {
		t.Fatalf("last_n budget: %d %v", forkTokens(out), err)
	}
}
