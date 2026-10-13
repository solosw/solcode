package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// batchTestContext wires a UseContext that dispatches child operations through
// the real atomic tools registered in this package.
func batchTestContext(t *testing.T) (*UseContext, string) {
	t.Helper()
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Register(NewViewTool(), NewEditTool(), NewWriteTool(), NewGrepTool(), NewGlobTool(), NewLsTool(), NewBashTool())
	uctx := &UseContext{
		WorkDir:     dir,
		BatchLookup: reg.Find,
	}
	uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*ContentBlock, error) {
		selected := reg.Find(name)
		if selected == nil {
			return ErrorResult("tool not found: " + name), nil
		}
		return selected.Invoke(ctx, uctx, input)
	}
	return uctx, dir
}

func invokeBatch(t *testing.T, uctx *UseContext, body string) batchResponse {
	t.Helper()
	out, err := NewBatchTool().Invoke(context.Background(), uctx, json.RawMessage(body))
	if err != nil {
		t.Fatalf("batch invoke error: %v", err)
	}
	if out == nil {
		t.Fatal("nil batch output")
	}
	var resp batchResponse
	if err := json.Unmarshal([]byte(out.Text), &resp); err != nil {
		t.Fatalf("decode batch response: %v (%s)", err, out.Text)
	}
	return resp
}

func TestBatchPartialSuccessKeepsIndependentResults(t *testing.T) {
	uctx, dir := batchTestContext(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := invokeBatch(t, uctx, `{
		"operations": [
			{"id":"read_ok","tool":"View","input":{"path":"a.txt"}},
			{"id":"read_missing","tool":"View","input":{"path":"nope.txt"}}
		]
	}`)
	if resp.BatchID == "" {
		t.Fatal("batch_id must be set")
	}
	if resp.Status != "partial_success" {
		t.Fatalf("status = %s, want partial_success (%+v)", resp.Status, resp.Results)
	}
	if resp.Summary.Succeeded != 1 || resp.Summary.Failed != 1 {
		t.Fatalf("summary = %+v", resp.Summary)
	}
	// Results are returned in operation-ID order, not completion order.
	if resp.Results[0].ID != "read_ok" || resp.Results[1].ID != "read_missing" {
		t.Fatalf("result order = %s,%s", resp.Results[0].ID, resp.Results[1].ID)
	}
	if resp.Results[0].Output == nil || !strings.Contains(resp.Results[0].Output.Content, "hello") {
		t.Fatalf("successful output lost: %+v", resp.Results[0])
	}
	if resp.Results[1].Error == nil || resp.Results[1].Error.Code != "FILE_NOT_FOUND" {
		t.Fatalf("missing-file error = %+v", resp.Results[1].Error)
	}
	if resp.Results[1].Error.Retryable {
		t.Fatal("FILE_NOT_FOUND must not be retryable")
	}
}

func TestBatchDependencyFailureSkipsDownstream(t *testing.T) {
	uctx, _ := batchTestContext(t)
	resp := invokeBatch(t, uctx, `{
		"operations": [
			{"id":"first","tool":"View","input":{"path":"missing.txt"}},
			{"id":"second","tool":"View","input":{"path":"missing.txt"},"depends_on":["first"]}
		]
	}`)
	if resp.Results[1].Status != "skipped" {
		t.Fatalf("downstream status = %s, want skipped", resp.Results[1].Status)
	}
	if len(resp.Results[1].BlockedBy) != 1 || resp.Results[1].BlockedBy[0] != "first" {
		t.Fatalf("blocked_by = %#v", resp.Results[1].BlockedBy)
	}
}

func TestBatchRejectsCycleAndReportsUnknownTool(t *testing.T) {
	uctx, _ := batchTestContext(t)
	out, _ := NewBatchTool().Invoke(context.Background(), uctx, json.RawMessage(`{
		"operations":[
			{"id":"a","tool":"View","input":{"path":"x"},"depends_on":["b"]},
			{"id":"b","tool":"View","input":{"path":"x"},"depends_on":["a"]}
		]}`))
	if out == nil || !out.IsError {
		t.Fatalf("cycle must be rejected, got %+v", out)
	}
	out, _ = NewBatchTool().Invoke(context.Background(), uctx, json.RawMessage(`{
		"operations":[{"id":"a","tool":"WriteMemory","input":{}}]}`))
	if out == nil || out.IsError {
		t.Fatalf("unsupported operation must have a structured result, got %+v", out)
	}
	var resp batchResponse
	if err := json.Unmarshal([]byte(out.Text), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "failed" || resp.Results[0].Error == nil || resp.Results[0].Error.Code != "UNSUPPORTED_TOOL" {
		t.Fatalf("unsupported tool accepted: %+v", resp)
	}
}

func TestBatchAtomicCommitSingleFile(t *testing.T) {
	uctx, dir := batchTestContext(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := invokeBatch(t, uctx, `{
		"atomic": true,
		"operations": [
			{"id":"e1","tool":"Edit","input":{"path":"f.txt","old_string":"alpha","new_string":"beta"}}
		]
	}`)
	if resp.AtomicStatus != "committed" {
		t.Fatalf("atomic_status = %s, want committed (%+v)", resp.AtomicStatus, resp.Results)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(got) != "beta\n" {
		t.Fatalf("file = %q, want beta\\n", got)
	}
}

func TestBatchAtomicRollbackRestoresOriginal(t *testing.T) {
	uctx, dir := batchTestContext(t)
	good := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(good, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Second edit targets a different file that does not exist; Edit fails, so
	// the first edit must be rolled back.
	resp := invokeBatch(t, uctx, `{
		"atomic": true,
		"operations": [
			{"id":"e1","tool":"Edit","input":{"path":"good.txt","old_string":"alpha","new_string":"beta"}},
			{"id":"e2","tool":"Edit","input":{"path":"missing.txt","old_string":"x","new_string":"y"}}
		]
	}`)
	if resp.AtomicStatus != "rolled_back" {
		t.Fatalf("atomic_status = %s, want rolled_back (%+v)", resp.AtomicStatus, resp.Results)
	}
	if resp.Status != "failed" {
		t.Fatalf("rolled-back batch status = %s, want failed", resp.Status)
	}
	// The applied-then-rolled-back edit must not be reported as a durable success.
	if resp.Results[0].Status != "success" || !resp.Results[0].Reverted {
		t.Fatalf("reverted op = %+v, want success+reverted", resp.Results[0])
	}
	if resp.Summary.Reverted != 1 || resp.Summary.Succeeded != 0 {
		t.Fatalf("summary should count reverted not succeeded: %+v", resp.Summary)
	}
	got, _ := os.ReadFile(good)
	if string(got) != "alpha\n" {
		t.Fatalf("rollback failed: good.txt = %q, want alpha\\n", got)
	}
	if resp.Results[1].Status != "error" {
		t.Fatalf("failing op status = %s, want error", resp.Results[1].Status)
	}
}

func TestBatchAtomicRejectsBashButAllowsRepeatedFile(t *testing.T) {
	uctx, dir := batchTestContext(t)
	out, _ := NewBatchTool().Invoke(context.Background(), uctx, json.RawMessage(`{
		"atomic":true,
		"operations":[{"id":"b","tool":"Bash","input":{"command":"echo hi"}}]}`))
	if out == nil || !out.IsError || !strings.Contains(out.Text, "file edits") {
		t.Fatalf("atomic mode must reject Bash, got %+v", out)
	}
	out, _ = NewBatchTool().Invoke(context.Background(), uctx, json.RawMessage(`{
		"atomic":true,
		"operations":[
			{"id":"a","tool":"Write","input":{"path":"same.txt","content":"1"}},
			{"id":"b","tool":"Write","input":{"path":"same.txt","content":"2"}}
		]}`))
	if out == nil || out.IsError {
		t.Fatalf("atomic mode must allow repeated targets, got %+v", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "same.txt"))
	if err != nil || string(got) != "2" {
		t.Fatalf("repeated write = %q, %v", got, err)
	}
}

func TestClassifyChildErrorRetryability(t *testing.T) {
	cases := []struct {
		text      string
		wantCode  string
		retryable bool
	}{
		{"no such file or directory", "FILE_NOT_FOUND", false},
		{"old_string not found; it must match exactly", "EDIT_MISMATCH", false},
		{"old_string appears multiple times", "EDIT_MISMATCH", false},
		{"permission denied", "PERMISSION_DENIED", false},
		{"invalid parameters: missing field", "INVALID_INPUT", false},
		{"context deadline exceeded", "TRANSIENT", true},
	}
	for _, tc := range cases {
		code, retryable := classifyChildError(tc.text)
		if code != tc.wantCode || retryable != tc.retryable {
			t.Fatalf("classifyChildError(%q) = %s/%v, want %s/%v", tc.text, code, retryable, tc.wantCode, tc.retryable)
		}
	}
}
