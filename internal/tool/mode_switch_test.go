package tool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestModeSwitchToolStartsGoalFlow(t *testing.T) {
	started := false
	modeTool := NewModeSwitchToolWithGoal(
		func(_ context.Context, mode string) error {
			if mode != "goal" {
				t.Fatalf("mode = %q, want goal", mode)
			}
			return nil
		},
		func(_ context.Context, uctx *UseContext) error {
			started = uctx != nil && uctx.SessionID == "session-1"
			return nil
		},
	)
	result, err := modeTool.Invoke(context.Background(), &UseContext{SessionID: "session-1"}, json.RawMessage(`{"mode":"goal"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError || !started {
		t.Fatalf("result = %#v, started = %v", result, started)
	}
}

func TestModeSwitchToolInvokesConfiguredTransition(t *testing.T) {
	var requested string
	modeTool := NewModeSwitchTool(func(_ context.Context, mode string) error {
		requested = mode
		return nil
	})
	result, err := modeTool.Invoke(context.Background(), nil, json.RawMessage(`{"mode":"plan"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError || requested != "plan" {
		t.Fatalf("result = %#v, requested = %q", result, requested)
	}
}

func TestModeSwitchToolRejectsUnknownMode(t *testing.T) {
	modeTool := NewModeSwitchTool(func(context.Context, string) error { return nil })
	result, err := modeTool.Invoke(context.Background(), nil, json.RawMessage(`{"mode":"auto"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %#v, want validation error", result)
	}
}

func TestModeSwitchToolAcceptsCanonicalAndLegacyUltra(t *testing.T) {
	for _, input := range []string{"ultra", "ultrl", " ULTRA ", " UlTrL "} {
		t.Run(input, func(t *testing.T) {
			var requested string
			modeTool := NewModeSwitchTool(func(_ context.Context, mode string) error {
				requested = mode
				return nil
			})
			raw, _ := json.Marshal(map[string]string{"mode": input})
			result, err := modeTool.Invoke(context.Background(), nil, raw)
			if err != nil || result.IsError || requested != "ultra" {
				t.Fatalf("Invoke(%q) = %#v, %v; requested = %q", input, result, err, requested)
			}
		})
	}
	modeTool := NewModeSwitchTool(nil)
	properties := modeTool.InputSchema()["properties"].(map[string]any)
	modes := properties["mode"].(map[string]any)["enum"].([]string)
	var found bool
	for _, mode := range modes {
		found = found || mode == "ultra"
	}
	if !found {
		t.Fatalf("ModeSwitch schema missing ultra: %v", modes)
	}
}
