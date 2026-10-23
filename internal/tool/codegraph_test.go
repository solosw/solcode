package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type graphToolFake struct {
	name   string
	args   json.RawMessage
	closes int
}

func (f *graphToolFake) Start()       {}
func (f *graphToolFake) Close() error { f.closes++; return nil }
func (f *graphToolFake) CallTool(_ context.Context, name string, input json.RawMessage) (*ContentBlock, error) {
	f.name = name
	f.args = input
	return Result("graph result"), nil
}
func TestCodeGraphToolQueriesAndSafety(t *testing.T) {
	f := &graphToolFake{}
	tool := NewCodeGraphTool(f, t.TempDir())
	for _, raw := range []string{
		`{"action":"explore","query":"AuthService login","maxFiles":20}`,
		`{"action":"search","query":"main","kind":"function","limit":10}`,
		`{"action":"node","file":"src/main.go","symbolsOnly":true}`,
		`{"action":"callers","symbol":"main"}`,
		`{"action":"callees","symbol":"main"}`,
		`{"action":"impact","symbol":"main","depth":2}`,
		`{"action":"files","path":"src","pattern":"**/*.go"}`,
		`{"action":"status"}`,
	} {
		input := json.RawMessage(raw)
		if err := tool.ValidateInput(context.Background(), input); err != nil {
			t.Fatal(raw, err)
		}
		r, err := tool.Invoke(context.Background(), nil, input)
		if err != nil || r.IsError {
			t.Fatalf("%s %+v %v", raw, r, err)
		}
		if !tool.IsReadOnly(input) || tool.IsDestructive(input) || !tool.IsConcurrencySafe(input) {
			t.Fatal("wrong safety annotations")
		}
		var args map[string]any
		_ = json.Unmarshal(f.args, &args)
		if _, ok := args["action"]; ok {
			t.Fatal("forwarded wrapper field")
		}
		if _, ok := args["projectPath"]; ok {
			t.Fatal("forwarded project override")
		}
	}
}
func TestCodeGraphToolRejectsManagementOverridesAndInvalidFields(t *testing.T) {
	f := &graphToolFake{}
	tool := NewCodeGraphTool(f, t.TempDir())
	for _, raw := range []string{
		`{"action":"init"}`,
		`{"action":"explore","query":"x","projectPath":"/other"}`,
		`{"action":"status","sql":"delete from nodes"}`,
		`{"action":"explore","query":"x","limit":10}`,
		`{"action":"explore","query":"x","maxFiles":21}`,
		`{"action":"node","file":"../../outside"}`,
		`{"action":"node","file":"/outside"}`,
		`{"action":"node","file":"C:/outside"}`,
		`{"action":"node"}`,
		`{"action":"explore","query":""}`,
		`{"action":"search","query":3}`,
		`{"action":"impact","symbol":"main","depth":100}`,
		`{"action":"search","query":"x","limit":null}`,
		`{"action":"node","file":"x","includeCode":"true"}`,
	} {
		if tool.ValidateInput(context.Background(), json.RawMessage(raw)) == nil {
			t.Fatal("accepted", raw)
		}
		r, err := tool.Invoke(context.Background(), nil, json.RawMessage(raw))
		if err != nil || !r.IsError {
			t.Fatalf("%s %+v %v", raw, r, err)
		}
	}
	if f.name != "" {
		t.Fatal("invalid query reached backend")
	}
}
func TestCodeGraphToolRejectsSymlinkEscape(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	if err := os.Symlink(out, filepath.Join(root, "external")); err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(filepath.Join(out, "secret.go"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewCodeGraphTool(&graphToolFake{}, root)
	if tool.ValidateInput(context.Background(), json.RawMessage(`{"action":"node","file":"external/secret.go"}`)) == nil {
		t.Fatal("accepted symlink escape")
	}
}
