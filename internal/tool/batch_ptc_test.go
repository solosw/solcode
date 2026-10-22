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
	if !IsPlanModeBatch(ptc, nil) || IsPlanModeBatch(legacy, nil) || IsPlanModeBatch(NewViewTool(), nil) || IsPlanModeBatch(nil, nil) {
		t.Fatal("plan container exception must only apply to valid built-in exploration batches")
	}
	if ptc.IsReadOnly(json.RawMessage(`{"code":"console.log(1)"}`)) {
		t.Fatal("PTC container must not be classified as read-only")
	}
	props := ptc.InputSchema()["properties"].(map[string]any)
	if _, ok := props["code"]; !ok {
		t.Fatal("PTC schema missing code")
	}
	if _, ok := props["operations"]; ok {
		t.Fatal("PTC must not expose operations")
	}
}

func TestPlanModeBatchClassification(t *testing.T) {
	batch := NewBatchTool()
	for _, tc := range []struct {
		name, input    string
		plan, readOnly bool
	}{
		{"read", `{"operations":[{"tool":"View","input":{"path":"a.txt"}}]}`, true, true},
		{"bash", `{"operations":[{"tool":"Bash","input":{"command":"echo plan-bash"}}]}`, true, false},
		{"mixed exploration", `{"operations":[{"tool":"View","input":{"path":"a.txt"}},{"tool":"Bash","input":{"command":"echo plan-bash"}}]}`, true, false},
		{"write", `{"operations":[{"tool":"Write","input":{"path":"a.txt","content":"x"}}]}`, false, false},
		{"AskUser", `{"operations":[{"tool":"AskUser","input":{}}]}`, false, false},
		{"empty", `{"operations":[]}`, false, false},
		{"atomic", `{"atomic":true,"operations":[{"tool":"Bash","input":{"command":"echo plan-bash"}}]}`, false, false},
		{"invalid", `{`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := json.RawMessage(tc.input)
			if got := IsPlanModeBatch(batch, input); got != tc.plan {
				t.Errorf("plan batch=%v, want %v", got, tc.plan)
			}
			if got := batch.IsReadOnly(input); got != tc.readOnly {
				t.Errorf("read-only=%v, want %v", got, tc.readOnly)
			}
		})
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
