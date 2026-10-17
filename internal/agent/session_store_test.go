package agent

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	"testing"
)

func TestCompactionPreservesFullAuditAndSharedReferences(t *testing.T) {
	fork := Snapshot{Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("inherited"))}}
	r := &Record{Context: []int{-1}}
	response := sdk.NewAssistantMessage(sdk.NewTextBlock("old response"))
	followup := sdk.NewUserMessage(sdk.NewTextBlock("next task"))
	original := append(append([]sdk.MessageParam(nil), fork.Messages...), response, followup)
	if err := r.apply(Checkpoint{Snapshot: Snapshot{Messages: original}}, fork); err != nil {
		t.Fatal(err)
	}
	summary := sdk.NewUserMessage(sdk.NewTextBlock("summary"))
	if err := r.apply(Checkpoint{Snapshot: Snapshot{Messages: []sdk.MessageParam{summary, followup}}, Compacted: true}, fork); err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 3 {
		t.Fatalf("audit should contain old response, followup, new summary once, got %d", len(r.Messages))
	}
	active, err := r.Active(fork)
	if err != nil || len(active.Messages) != 2 {
		t.Fatalf("active: %+v %v", active, err)
	}
	if messageKey(r.Messages[0]) != messageKey(response) {
		t.Fatal("old response discarded")
	}
}

func TestForkModesAndAllowlistIntersection(t *testing.T) {
	parent := Snapshot{Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("first")), sdk.NewAssistantMessage(sdk.NewTextBlock("response")), sdk.NewUserMessage(sdk.NewTextBlock("last"))}, WorkingSet: []byte("{}")}
	for _, tc := range []struct {
		mode    string
		n       int
		working bool
	}{{"none", 0, false}, {"full", 3, true}, {"last_n", 1, true}, {"working_set", 0, true}} {
		out, err := selectFork(AgentConfig{ForkContext: tc.mode, ForkLastN: 1, ParentContext: parent})
		if err != nil || len(out.Messages) != tc.n || (len(out.WorkingSet) > 0) != tc.working {
			t.Fatalf("%s: %+v %v", tc.mode, out, err)
		}
	}
	if got := IntersectTools([]string{"View"}, []string{"Write"}); len(got) != 1 || got[0] != "__no_tools__" {
		t.Fatalf("empty intersection became unrestricted: %v", got)
	}
	if got := IntersectTools([]string{"View"}, nil); len(got) != 1 || got[0] != "View" {
		t.Fatalf("ceiling lost: %v", got)
	}
}
