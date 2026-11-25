package tool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSolAPIScopeAndActions(t *testing.T) {
	api := NewSolAPITool()
	calls := 0
	scope := &UseContext{SessionID: "current", WorkDir: "/project", AgentRole: "main", SolAPI: func(_ context.Context, req SolAPIRequest) (any, error) {
		calls++
		return map[string]string{"action": req.Action}, nil
	}}
	for _, input := range []string{`{"action":"register","code":"await sol.sleep(25); sol.trigger('event')"}`, `{"action":"cancel"}`, `{"action":"history"}`} {
		result, err := api.Invoke(t.Context(), scope, json.RawMessage(input))
		if err != nil || result.IsError {
			t.Fatalf("%s: %v %+v", input, err, result)
		}
	}
	for _, input := range []string{`{"action":"status"}`, `{"action":"end_session"}`, `{"action":"register"}`, `{"action":"cancel","session_id":"other"}`, `{"action":"history","limit":101}`, `{"action":"register","code":"x","permission_mode":"ultra"}`} {
		result, err := api.Invoke(t.Context(), scope, json.RawMessage(input))
		if err != nil || !result.IsError {
			t.Fatalf("accepted invalid request %s: %+v %v", input, result, err)
		}
	}
	child := *scope
	child.AgentRole = "sub"
	result, err := api.Invoke(t.Context(), &child, json.RawMessage(`{"action":"cancel"}`))
	if err != nil || !result.IsError {
		t.Fatalf("accepted child: %+v %v", result, err)
	}
	if calls != 3 {
		t.Fatalf("host calls=%d", calls)
	}
	if !api.IsReadOnly(json.RawMessage(`{"action":"history"}`)) || api.IsReadOnly(json.RawMessage(`{"action":"cancel"}`)) {
		t.Fatal("wrong permission classification")
	}
}
