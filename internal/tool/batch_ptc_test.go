package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBatchPTCSwitchSchema(t *testing.T) {
	legacy := NewBatchToolWithPTC(DefaultBatchLimits, false)
	ptc := NewBatchToolWithPTC(DefaultBatchLimits, true)
	if _, ok := legacy.InputSchema()["properties"].(map[string]any)["operations"]; !ok {
		t.Fatal("legacy BatchTool schema lost operations")
	}
	props := ptc.InputSchema()["properties"].(map[string]any)
	if _, ok := props["code"]; !ok {
		t.Fatal("PTC schema missing code")
	}
	if _, ok := props["operations"]; ok {
		t.Fatal("PTC must not expose operations")
	}
}

func TestBatchPTCProgramFlow(t *testing.T) {
	ptc := NewBatchToolWithPTC(DefaultBatchLimits, true)
	calls := 0
	uctx := &UseContext{
		BatchLookup: func(name string) Tool {
			if name == GrepToolName {
				return NewGrepTool()
			}
			return nil
		},
		BatchDispatch: func(ctx context.Context, name string, input json.RawMessage) (*ContentBlock, error) {
			calls++
			if name != GrepToolName {
				t.Fatalf("unexpected tool %q", name)
			}
			return Result("found file.go"), nil
		},
	}
	input, _ := json.Marshal(map[string]string{"code": `const a = await tools.Grep({pattern:"find"}); if(a.ok) console.log(a.content.toUpperCase());`})
	result, err := ptc.Invoke(context.Background(), uctx, input)
	if err != nil || result == nil || result.IsError {
		t.Fatalf("PTC failed: %v / %+v", err, result)
	}
	if calls != 1 || !strings.Contains(result.Text, "FOUND FILE.GO") {
		t.Fatalf("unexpected result calls=%d result=%q", calls, result.Text)
	}
}

func TestBatchPTCNoImplicitEnvironment(t *testing.T) {
	ptc := NewBatchToolWithPTC(DefaultBatchLimits, true)
	uctx := &UseContext{BatchLookup: func(string) Tool { return nil }, BatchDispatch: func(context.Context, string, json.RawMessage) (*ContentBlock, error) {
		return Result("unexpected"), nil
	}}
	input := json.RawMessage(`{"code":"console.log(typeof require, typeof process, typeof fetch)"}`)
	result, err := ptc.Invoke(context.Background(), uctx, input)
	if err != nil || result.IsError {
		t.Fatalf("PTC failed: %v %+v", err, result)
	}
	if !strings.Contains(result.Text, "undefined undefined undefined") {
		t.Fatalf("unexpected globals %q", result.Text)
	}
}
