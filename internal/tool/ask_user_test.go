package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func sampleAskUserInput() json.RawMessage {
	return json.RawMessage(`{
		"questions": [{
			"question": "Which approach?",
			"header": "scope",
			"options": [
				{"label": "Fast", "description": "ship quickly"},
				{"label": "Safe", "description": "be careful"}
			]
		}]
	}`)
}

func TestAskUserNestedAgentAutoSelectsWithoutCallback(t *testing.T) {
	toolImpl := NewAskUserTool()
	called := false
	block, err := toolImpl.Invoke(context.Background(), &UseContext{
		AgentRole: "task",
		AskUser: func(context.Context, AskUserParams) (map[string]string, error) {
			called = true
			return nil, errors.New("should not prompt")
		},
	}, sampleAskUserInput())
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if called {
		t.Fatal("nested AskUser must not call the interactive callback")
	}
	if block == nil || block.IsError {
		t.Fatalf("unexpected result: %+v", block)
	}
	if !strings.Contains(block.Text, "A: Fast") {
		t.Fatalf("expected first-option fallback, got %q", block.Text)
	}
}

func TestAskUserNestedAgentUsesAutoSelectCallback(t *testing.T) {
	toolImpl := NewAskUserTool()
	block, err := toolImpl.Invoke(context.Background(), &UseContext{
		AgentRole: "sub",
		AskUserAutoSelect: func(context.Context, AskUserParams) (map[string]string, error) {
			return map[string]string{"Which approach?": "Safe"}, nil
		},
	}, sampleAskUserInput())
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if block == nil || !strings.Contains(block.Text, "A: Safe") {
		t.Fatalf("expected Jev-selected answer, got %+v", block)
	}
}

func TestAskUserTimeoutFallsBackToAutoSelect(t *testing.T) {
	ask := NewAskUserTool().(*askUserTool)
	ask.timeoutSecs = 1
	status := ""
	block, err := ask.Invoke(context.Background(), &UseContext{
		AskUser: func(ctx context.Context, _ AskUserParams) (map[string]string, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		AskUserAutoSelect: func(context.Context, AskUserParams) (map[string]string, error) {
			return map[string]string{"Which approach?": "Safe"}, nil
		},
		Status: func(s string) { status = s },
	}, sampleAskUserInput())
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if block == nil || !strings.Contains(block.Text, "A: Safe") {
		t.Fatalf("expected timeout auto-select, got %+v", block)
	}
	if !strings.Contains(status, "timed out") {
		t.Fatalf("expected timeout status, got %q", status)
	}
}

func TestAskUserInteractiveStillWorks(t *testing.T) {
	toolImpl := NewAskUserTool()
	block, err := toolImpl.Invoke(context.Background(), &UseContext{
		AskUser: func(context.Context, AskUserParams) (map[string]string, error) {
			return map[string]string{"Which approach?": "Fast"}, nil
		},
	}, sampleAskUserInput())
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if block == nil || !strings.Contains(block.Text, "A: Fast") {
		t.Fatalf("expected interactive answer, got %+v", block)
	}
}

func TestAskUserParentCancelDoesNotAutoSelect(t *testing.T) {
	ask := NewAskUserTool().(*askUserTool)
	ask.timeoutSecs = 30
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	autoCalled := false
	block, err := ask.Invoke(ctx, &UseContext{
		AskUser: func(askCtx context.Context, _ AskUserParams) (map[string]string, error) {
			return nil, askCtx.Err()
		},
		AskUserAutoSelect: func(context.Context, AskUserParams) (map[string]string, error) {
			autoCalled = true
			return map[string]string{"Which approach?": "Safe"}, nil
		},
	}, sampleAskUserInput())
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if autoCalled {
		t.Fatal("parent cancel must not trigger Jev auto-select")
	}
	if block == nil || !block.IsError || !strings.Contains(block.Text, "cancelled") {
		t.Fatalf("expected cancel error, got %+v", block)
	}
}

func TestDefaultAskUserAnswers(t *testing.T) {
	answers := DefaultAskUserAnswers(AskUserParams{Questions: []Question{{
		Question: "Q?",
		Options:  []QuestionOption{{Label: "A"}, {Label: "B"}},
	}}})
	if answers["Q?"] != "A" {
		t.Fatalf("answers = %#v", answers)
	}
}

func TestAskUserTimeoutUsesConfiguredSeconds(t *testing.T) {
	if AskUserTimeout != 300 {
		t.Fatalf("AskUserTimeout = %d, want 300", AskUserTimeout)
	}
	_ = time.Second
}
