package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestBatchSchemaExposesRealChildInputs(t *testing.T) {
	data, _ := json.Marshal(NewBatchTool().InputSchema())
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		body  string
		valid bool
	}{
		{`{"operations":[{"tool":"Edit","input":{"path":"x","old_string":"a","new_string":"b"}}]}`, true},
		{`{"operations":[{"tool":"Edit","input":{"path":"x","old_string":"a"}}]}`, false},
		{`{"operations":[{"tool":"MultiEdit","input":{"atomic":true,"edits":[{"path":"x","old_string":"a","new_string":"b"}]}}]}`, true},
		{`{"operations":[{"tool":"Grep","input":{"command":"rg foo"}}]}`, false},
		{`{"operations":[{"tool":"LSP","input":{"operation":"hover","path":"x","line":1,"character":1}}]}`, true},
	}
	for _, tc := range cases {
		var value any
		_ = json.Unmarshal([]byte(tc.body), &value)
		err := resolved.Validate(value)
		if (err == nil) != tc.valid {
			t.Errorf("%s: valid=%v, error=%v", tc.body, tc.valid, err)
		}
	}
}

func TestBatchInvalidChildDoesNotEraseIndependentSuccess(t *testing.T) {
	uctx, dir := batchTestContext(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("keep me"), 0600)
	resp := invokeBatch(t, uctx, `{"operations":[
  {"id":"bad","tool":"Edit","input":{"path":"a.txt","old_string":"keep me"}},
  {"id":"dependent","tool":"Write","input":{"path":"new.txt","content":"oops"},"depends_on":["bad"]},
  {"tool":"View","input":{"path":"a.txt"}}
 ]}`)
	if resp.Status != "partial_success" || resp.Results[0].Error.Code != "INVALID_INPUT" || resp.Results[1].Status != "skipped" || resp.Results[2].ID != "op_3" {
		t.Fatalf("response = %+v", resp)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "keep me" {
		t.Fatalf("invalid edit changed file: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("dependent write ran")
	}
}

func TestBatchRejectsUnknownChildFieldsAndNonObjects(t *testing.T) {
	for _, input := range []string{`{"file_path":"x"}`, `{"path":"x","offset":1,"limti":3}`, `null`, `[]`} {
		t.Run(input, func(t *testing.T) {
			uctx, _ := batchTestContext(t)
			called := false
			uctx.BatchDispatch = func(context.Context, string, json.RawMessage) (*ContentBlock, error) {
				called = true
				return Result("unexpected"), nil
			}
			resp := invokeBatch(t, uctx, fmt.Sprintf(`{"operations":[{"tool":"View","input":%s}]}`, input))
			if called || resp.Results[0].Status != "error" || resp.Results[0].Error.Code != "INVALID_INPUT" {
				t.Fatalf("called=%v, response=%+v", called, resp)
			}
		})
	}
}

func TestBatchTotalBudgetPreservesRecoverableFullResults(t *testing.T) {
	uctx, _ := batchTestContext(t)
	full := strings.Repeat("你好\n", 100)
	uctx.BatchDispatch = func(_ context.Context, _ string, input json.RawMessage) (*ContentBlock, error) {
		if strings.Contains(string(input), "bad") {
			return ErrorResult(full), nil
		}
		return Result(full), nil
	}
	out, err := NewBatchToolWithLimits(BatchLimits{MaxResultRunes: 50, MaxTotalResultRunes: 80}).Invoke(context.Background(), uctx, json.RawMessage(`{"operations":[
  {"tool":"View","input":{"path":"a"}},{"tool":"View","input":{"path":"b"}},{"tool":"View","input":{"path":"bad"}}
 ]}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp batchResponse
	if err := json.Unmarshal([]byte(out.Text), &resp); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, r := range resp.Results {
		text, path := "", ""
		if r.Output != nil {
			text, path = r.Output.Content, r.Output.FullOutputPath
			if !r.Output.Truncated {
				t.Fatal("missing output truncation flag")
			}
		} else {
			text, path = r.Error.Message, r.Error.FullOutputPath
			if !r.Error.Truncated {
				t.Fatal("missing error truncation flag")
			}
		}
		total += len([]rune(text))
		if len([]rune(text)) > 50 {
			t.Fatal("per-item budget exceeded")
		}
		if path == "" {
			t.Fatalf("full output missing: %+v", r)
		}
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
		data, err := os.ReadFile(path)
		if err != nil || string(data) != full {
			t.Fatalf("archive = %q, %v", data, err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe artifact permissions: %v", info.Mode())
		}
		view, err := NewViewTool().Invoke(context.Background(), uctx, json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":0,"limit":2}`, path)))
		if err != nil || view.IsError || !strings.Contains(view.Text, "你好") {
			t.Fatalf("archive not retrievable: %+v, %v", view, err)
		}
	}
	if total > 80 {
		t.Fatalf("total text budget exceeded: %d", total)
	}
}

func TestBatchCompactWritesAndFullOptIn(t *testing.T) {
	for _, mode := range []string{"compact", "full"} {
		t.Run(mode, func(t *testing.T) {
			uctx, dir := batchTestContext(t)
			out, err := NewBatchTool().Invoke(context.Background(), uctx, json.RawMessage(fmt.Sprintf(`{"output_mode":%q,"operations":[{"tool":"Write","input":{"path":"a.txt","content":"new\n"}}]}`, mode)))
			if err != nil {
				t.Fatal(err)
			}
			var resp batchResponse
			_ = json.Unmarshal([]byte(out.Text), &resp)
			r := resp.Results[0].Output
			if !strings.Contains(r.Content, "Lines:") {
				t.Fatalf("missing change counts: %s", r.Content)
			}
			if mode == "compact" {
				if !r.Summarized || strings.Contains(r.Content, "---") || r.FullOutputPath == "" {
					t.Fatalf("not compact: %+v", r)
				}
				t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(r.FullOutputPath)) })
				data, _ := os.ReadFile(r.FullOutputPath)
				if !strings.Contains(string(data), "new") {
					t.Fatal("diff lost")
				}
			} else if r.Summarized || !strings.Contains(r.Content, "new") {
				t.Fatalf("full output lost: %+v", r)
			}
			data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
			if string(data) != "new\n" {
				t.Fatal("write missing")
			}
		})
	}
}

func TestBatchLargeReadWaveStillRunsAtBoundedParallelism(t *testing.T) {
	uctx, _ := batchTestContext(t)
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	var active, peak atomic.Int32
	uctx.BatchDispatch = func(context.Context, string, json.RawMessage) (*ContentBlock, error) {
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return Result("ok"), nil
	}
	operations := make([]batchOperation, 6)
	for i := range operations {
		operations[i] = batchOperation{ID: fmt.Sprint(i), Tool: "View", Input: json.RawMessage(`{"path":"x"}`)}
	}
	input, _ := json.Marshal(batchRequest{Operations: operations})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = NewBatchToolWithLimits(BatchLimits{MaxParallel: 2}).Invoke(context.Background(), uctx, input)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			<-done
			t.Fatal("large read batch became serial")
		}
	}
	close(release)
	<-done
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency=%d, want 2", peak.Load())
	}
}

func TestBatchAtomicRepeatedFileRollsBackOriginal(t *testing.T) {
	uctx, dir := batchTestContext(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original"), 0600)
	resp := invokeBatch(t, uctx, `{"atomic":true,"operations":[
  {"id":"first","tool":"Edit","input":{"path":"a.txt","old_string":"original","new_string":"second"}},
  {"id":"second","tool":"Edit","input":{"path":"./a.txt","old_string":"second","new_string":"third"}},
  {"id":"bad","tool":"Edit","input":{"path":"a.txt","old_string":"missing","new_string":"oops"}}
 ]}`)
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "original" || resp.AtomicStatus != "rolled_back" || resp.Summary.Reverted != 2 {
		t.Fatalf("got=%q response=%+v", got, resp)
	}
}

func TestBatchTruncationKeepsTailWithinBudget(t *testing.T) {
	text := "FIRST\n" + strings.Repeat("body\n", 100) + "LAST exit=7"
	got := truncateBatchText([]rune(text), 80)
	if len([]rune(got)) > 80 || !strings.HasPrefix(got, "FIRST") || !strings.HasSuffix(got, "LAST exit=7") || !strings.Contains(got, "truncated") {
		t.Fatalf("unexpected truncation: %q", got)
	}
	for budget := 0; budget < 30; budget++ {
		if got := truncateBatchText([]rune(text), budget); len([]rune(got)) > budget {
			t.Fatalf("budget=%d: %q", budget, got)
		}
	}
}

func TestBatchArchiveFailureNeverInventsPath(t *testing.T) {
	uctx, _ := batchTestContext(t)
	uctx.TextFileSystem = &batchMemoryFS{files: map[string]string{}, readOnly: true}
	uctx.BatchDispatch = func(context.Context, string, json.RawMessage) (*ContentBlock, error) {
		return Result(strings.Repeat("long\n", 100)), nil
	}
	out, err := NewBatchToolWithLimits(BatchLimits{MaxTotalResultRunes: 80}).Invoke(context.Background(), uctx, json.RawMessage(`{"operations":[{"tool":"View","input":{"path":"x"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp batchResponse
	_ = json.Unmarshal([]byte(out.Text), &resp)
	if resp.OutputStorageError == "" || resp.Results[0].Output.FullOutputPath != "" || !resp.Results[0].Output.Truncated {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestBatchACPArchiveUsesReadableClientFilesystem(t *testing.T) {
	uctx, _ := batchTestContext(t)
	fs := &batchMemoryFS{files: map[string]string{}}
	uctx.TextFileSystem = fs
	full := strings.Repeat("client text\n", 100)
	uctx.BatchDispatch = func(context.Context, string, json.RawMessage) (*ContentBlock, error) { return Result(full), nil }
	out, err := NewBatchToolWithLimits(BatchLimits{MaxTotalResultRunes: 80}).Invoke(context.Background(), uctx, json.RawMessage(`{"operations":[{"tool":"View","input":{"path":"x"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp batchResponse
	_ = json.Unmarshal([]byte(out.Text), &resp)
	path := resp.Results[0].Output.FullOutputPath
	if path == "" || fs.files[path] != full || resp.OutputStorageError != "" {
		t.Fatalf("archive missing: %+v", resp)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
	view, err := NewViewTool().Invoke(context.Background(), uctx, json.RawMessage(fmt.Sprintf(`{"path":%q,"limit":1}`, path)))
	if err != nil || view.IsError || !strings.Contains(view.Text, "client text") {
		t.Fatalf("client archive unreadable: %+v %v", view, err)
	}
}

func TestBatchACPRollbackCannotDeleteLocalFile(t *testing.T) {
	uctx, dir := batchTestContext(t)
	// The client reports this path absent, but it exists in the local namespace.
	path := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(path, []byte("local file must survive"), 0600)
	fs := &batchMemoryFS{files: map[string]string{}}
	uctx.TextFileSystem = fs
	resp := invokeBatch(t, uctx, `{"atomic":true,"operations":[
  {"id":"create","tool":"Write","input":{"path":"a.txt","content":"remote"}},
  {"id":"bad","tool":"Edit","input":{"path":"a.txt","old_string":"missing","new_string":"oops"}}
 ]}`)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "local file must survive" || resp.AtomicStatus != "rollback_failed" {
		t.Fatalf("local=%q error=%v response=%+v", got, err, resp)
	}
}

func TestBatchUnknownToolKeepsIndependentSuccess(t *testing.T) {
	uctx, _ := batchTestContext(t)
	resp := invokeBatch(t, uctx, `{"operations":[
  {"id":"bad","tool":"read_file","input":{"path":"a.txt"}},
  {"id":"ok","tool":"Write","input":{"path":"a.txt","content":"kept"}},
  {"id":"blocked","tool":"View","input":{"path":"a.txt"},"depends_on":["bad"]}
 ]}`)
	if resp.Status != "partial_success" || resp.Results[0].Error.Code != "UNSUPPORTED_TOOL" || resp.Results[1].Status != "success" || resp.Results[2].Status != "skipped" {
		t.Fatalf("response=%+v", resp)
	}
}

func TestBatchReadOnlyPermissionAnnotation(t *testing.T) {
	cases := []struct {
		body     string
		readOnly bool
	}{
		{`{"operations":[{"tool":"View","input":{"path":"x"}},{"tool":"Grep","input":{"pattern":"x"}}]}`, true},
		{`{"operations":[{"tool":"LSP","input":{"operation":"hover"}}]}`, true},
		{`{"operations":[{"tool":"View","input":{"path":"x"}},{"tool":"Write","input":{"path":"x","content":"x"}}]}`, false},
		{`{"operations":[{"tool":"Bash","input":{"command":"ls"}}]}`, false},
		{`{"atomic":true,"operations":[{"tool":"View","input":{"path":"x"}}]}`, false},
		{`{"operations":[]}`, false},
	}
	for _, tc := range cases {
		if got := NewBatchTool().IsReadOnly(json.RawMessage(tc.body)); got != tc.readOnly {
			t.Errorf("%s: readOnly=%v", tc.body, got)
		}
	}
}
