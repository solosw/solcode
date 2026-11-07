package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/contextcompression"
)

type searchMemoryReader struct {
	req    MemoryReadRequest
	result MemoryReadResult
	err    error
	calls  int
}

func (r *searchMemoryReader) ReadMemory(_ context.Context, req MemoryReadRequest) (MemoryReadResult, error) {
	r.req = req
	r.calls++
	return r.result, r.err
}

type searchSessionReader struct {
	req    SessionMemoryReadRequest
	result SessionMemoryReadResult
	err    error
	calls  int
}

func (r *searchSessionReader) ReadSessionMemory(_ context.Context, req SessionMemoryReadRequest) (SessionMemoryReadResult, error) {
	r.req = req
	r.calls++
	return r.result, r.err
}

func invokeSearch(t *testing.T, search Tool, u *UseContext, input string) map[string]struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
} {
	t.Helper()
	block, err := search.Invoke(t.Context(), u, json.RawMessage(input))
	if err != nil {
		t.Fatal(err)
	}
	if block.IsError {
		t.Fatalf("search failed: %s", block.Text)
	}
	var out struct {
		Sources map[string]struct {
			Status string          `json:"status"`
			Data   json.RawMessage `json:"data"`
			Error  string          `json:"error"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(block.Text), &out); err != nil {
		t.Fatal(err)
	}
	return out.Sources
}

func TestSearchContextCombinesSourcesAndPreservesSession(t *testing.T) {
	memory := &searchMemoryReader{result: MemoryReadResult{Entries: []MemoryEntry{{Text: "remember build command", OtherSession: true}}, CrossSessionAllowed: true}}
	session := &searchSessionReader{result: SessionMemoryReadResult{Entries: []SessionMemoryEntry{{Summary: "verified build command", SessionID: "child-session"}}}}
	var contextReq contextcompression.Request
	u := &UseContext{SessionID: "child-session", WorkDir: "/work/project", ContextControl: func(_ context.Context, name string, req contextcompression.Request) (any, error) {
		if name != SearchContextToolName {
			t.Fatalf("operation = %s", name)
		}
		contextReq = req
		return map[string]any{"matches": []map[string]any{{"ref": "m12"}}, "next_offset": 3}, nil
	}}
	out := invokeSearch(t, NewSearchContextTool(memory, session), u, `{"query":"  build command  ","kind":"Workflow","scope":"PROJECT","offset":2,"limit":50}`)
	if len(out) != 3 || out["context"].Status != "ok" || out["memory"].Status != "ok" || out["session_memory"].Status != "ok" {
		t.Fatalf("sources = %+v", out)
	}
	if contextReq.Query != "build command" || contextReq.Offset != 2 || contextReq.Limit != 50 || !strings.Contains(string(out["context"].Data), "m12") {
		t.Fatalf("context = %+v, %s", contextReq, out["context"].Data)
	}
	if memory.req.SessionID != u.SessionID || memory.req.WorkDir != u.WorkDir || memory.req.Kind != "workflow" || memory.req.Scope != "project" || memory.req.Limit != 25 || memory.req.Query != "build command" {
		t.Fatalf("memory request = %+v", memory.req)
	}
	if session.req.SessionID != u.SessionID || session.req.WorkDir != u.WorkDir || session.req.Limit != 25 || session.req.Query != "build command" {
		t.Fatalf("session request = %+v", session.req)
	}
	if !strings.Contains(string(out["memory"].Data), "other-session") {
		t.Fatal("lost cross-session labeling")
	}
}

func TestSearchContextEmptyQueryAndPartialFailures(t *testing.T) {
	memory := &searchMemoryReader{result: MemoryReadResult{Beliefs: []string{"known command"}}}
	session := &searchSessionReader{err: errors.New("recall disk unavailable")}
	u := &UseContext{ContextControl: func(context.Context, string, contextcompression.Request) (any, error) {
		t.Fatal("empty query must not search context")
		return nil, nil
	}}
	out := invokeSearch(t, NewSearchContextTool(memory, session), u, "{}")
	if out["context"].Status != "skipped" || out["memory"].Status != "ok" || out["session_memory"].Status != "error" || !strings.Contains(out["session_memory"].Error, "recall disk unavailable") {
		t.Fatalf("sources = %+v", out)
	}
	if memory.req.Limit != 8 || session.req.Limit != 5 || !strings.Contains(string(out["memory"].Data), "known command") {
		t.Fatalf("lost defaults or beliefs: %+v", out)
	}
	if !strings.Contains(string(out["memory"].Data), "opted out") {
		t.Fatal("lost cross-session opt-out notice")
	}
	out = invokeSearch(t, NewSearchContextTool(nil, nil), nil, `{"query":"command"}`)
	for name, result := range out {
		if result.Status != "unavailable" {
			t.Fatalf("%s = %+v", name, result)
		}
	}
}

func TestSearchContextSourceSelection(t *testing.T) {
	for _, source := range []string{"context", "memory", "session_memory"} {
		t.Run(source, func(t *testing.T) {
			memory := &searchMemoryReader{}
			session := &searchSessionReader{}
			contextCalls := 0
			u := &UseContext{ContextControl: func(context.Context, string, contextcompression.Request) (any, error) {
				contextCalls++
				return map[string]any{"matches": []any{}}, nil
			}}
			out := invokeSearch(t, NewSearchContextTool(memory, session), u, `{"query":"command","source":"`+source+`"}`)
			if len(out) != 1 || out[source].Status != "ok" {
				t.Fatalf("sources = %+v", out)
			}
			for name, calls := range map[string]int{"context": contextCalls, "memory": memory.calls, "session_memory": session.calls} {
				expected := 0
				if name == source {
					expected = 1
				}
				if calls != expected {
					t.Fatalf("%s called %d times", name, calls)
				}
			}
		})
	}
}

func TestSearchContextRejectsInvalidParametersBeforeReading(t *testing.T) {
	for _, input := range []string{
		`{"source":"foreign-session"}`, `{"source":"context"}`, `{"kind":"invalid"}`, `{"scope":"invalid"}`,
		`{"query":"q","offset":-1}`, `{"limit":-1}`, `{"source":"session_memory","kind":"fact"}`,
		`{"source":"memory","offset":1}`, `{"source":`,
	} {
		t.Run(input, func(t *testing.T) {
			memory := &searchMemoryReader{}
			session := &searchSessionReader{}
			b, err := NewSearchContextTool(memory, session).Invoke(t.Context(), nil, json.RawMessage(input))
			if err != nil || !b.IsError {
				t.Fatalf("result=%+v, err=%v", b, err)
			}
			if memory.calls != 0 || session.calls != 0 {
				t.Fatal("invalid parameters triggered reads")
			}
		})
	}
}
