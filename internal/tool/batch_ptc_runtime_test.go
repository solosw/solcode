package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func runPTCTest(t *testing.T, ctx context.Context, limits BatchLimits, uctx *UseContext, code string) *ContentBlock {
	t.Helper()
	input, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewBatchToolWithPTC(limits, true).Invoke(ctx, uctx, input)
	if err != nil || result == nil {
		t.Fatalf("PTC failed: %v / %+v", err, result)
	}
	return result
}

func TestBatchPTCParallelReadsBounded(t *testing.T) {
	uctx, _ := batchTestContext(t)
	var active, peak atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*ContentBlock, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			return Result("read"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	limits := DefaultBatchLimits
	limits.MaxParallel = 2
	input := json.RawMessage(`{"code":"const r = await Promise.all([1,2,3,4].map(i => tools.View({path:String(i)}))); console.log(r.map(x=>x.content).join(','));"}`)
	done := make(chan *ContentBlock, 1)
	go func() {
		result, _ := NewBatchToolWithPTC(limits, true).Invoke(ctx, uctx, input)
		done <- result
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			close(release)
			t.Fatal("reads did not execute concurrently")
		}
	}
	if got := peak.Load(); got != 2 {
		t.Errorf("peak concurrency = %d, want 2", got)
	}
	close(release)
	select {
	case result := <-done:
		if result == nil || result.IsError || result.Text != "read,read,read,read" {
			t.Fatalf("unexpected result: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("PTC did not finish")
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("parallel limit exceeded: %d", got)
	}
}

func TestBatchPTCWritesAreBarriers(t *testing.T) {
	uctx, _ := batchTestContext(t)
	var active atomic.Int32
	var overlap atomic.Bool
	var mu sync.Mutex
	var order []string
	uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*ContentBlock, error) {
		if active.Add(1) != 1 {
			overlap.Store(true)
		}
		defer active.Add(-1)
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		return Result(name), nil
	}
	result := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, `
		const r = await Promise.all([
		  tools.View({path:"before"}), tools.Write({path:"file",content:"x"}),
		  tools.View({path:"after"}), tools.Bash({command:"echo x"})
		]); console.log(r.every(x=>x.ok));`)
	if result.IsError || result.Text != "true" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if overlap.Load() || strings.Join(order, ",") != "View,Write,View,Bash" {
		t.Fatalf("unsafe overlap/order: %v / %v", overlap.Load(), order)
	}
}

func TestBatchPTCDependentCallsUseResolvedValues(t *testing.T) {
	uctx, _ := batchTestContext(t)
	var paths []string
	uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*ContentBlock, error) {
		var args struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(input, &args)
		paths = append(paths, args.Path)
		return Result(args.Path + "-next"), nil
	}
	result := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, `
		const first = await tools.View({path:"start"});
		const next = await tools.View({path:first.content}); console.log(next.content);`)
	if result.IsError || result.Text != "start-next-next" || strings.Join(paths, ",") != "start,start-next" {
		t.Fatalf("unexpected dependency result: %+v / %v", result, paths)
	}
}

func TestBatchPTCChildFailuresAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, code, want string
		dispatches       int
	}{
		{"unknown field", `console.log((await tools.View({file_path:"x"})).error)`, "INVALID_INPUT", 0},
		{"null", `console.log((await tools.View(null)).error)`, "INVALID_INPUT", 0},
		{"cycle", `const x={}; x.self=x; console.log((await tools.View(x)).error)`, "INVALID_INPUT", 0},
		{"missing args", `console.log((await tools.View()).error)`, "INVALID_INPUT", 0},
		{"unavailable", `console.log((await tools.LSP({operation:"document_symbol",path:"x"})).error)`, "TOOL_UNAVAILABLE", 0},
		{"denied", `const r=await tools.View({path:"deny"}); console.log(r.ok,r.content)`, "false permission denied", 1},
		{"transport", `console.log((await tools.View({path:"error"})).error)`, "dispatcher failed", 1},
		{"panic", `console.log((await tools.View({path:"panic"})).error)`, "PTC child panic", 1},
		{"empty", `console.log((await tools.View({path:"empty"})).error)`, "EMPTY_RESULT", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uctx, _ := batchTestContext(t)
			var calls atomic.Int32
			uctx.BatchDispatch = func(ctx context.Context, name string, input json.RawMessage) (*ContentBlock, error) {
				calls.Add(1)
				switch {
				case strings.Contains(string(input), "deny"):
					return ErrorResult("permission denied"), nil
				case strings.Contains(string(input), "error"):
					return nil, fmt.Errorf("dispatcher failed")
				case strings.Contains(string(input), "panic"):
					panic("test panic")
				default:
					return nil, nil
				}
			}
			result := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, tc.code)
			if result.IsError || !strings.Contains(result.Text, tc.want) || int(calls.Load()) != tc.dispatches {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
		})
	}
}

func TestBatchPTCCallBudget(t *testing.T) {
	uctx, _ := batchTestContext(t)
	var calls atomic.Int32
	uctx.BatchDispatch = func(context.Context, string, json.RawMessage) (*ContentBlock, error) {
		calls.Add(1)
		return Result("ok"), nil
	}
	result := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, `
		const r = await Promise.all(Array.from({length:40},()=>tools.View({path:"x"})));
		console.log(r.filter(x=>x.ok).length,r.filter(x=>x.error==="PTC_CALL_LIMIT").length);`)
	if result.IsError || result.Text != "32 8" || calls.Load() != ptcMaxCalls {
		t.Fatalf("budget result=%+v calls=%d", result, calls.Load())
	}
}

func TestBatchPTCCancellation(t *testing.T) {
	for _, code := range []string{`while(true){}`, `await tools.View({path:"wait"})`, `await Promise.resolve(); while(true){}`, `await tools.View({path:"fast"}); while(true){}`} {
		t.Run(code, func(t *testing.T) {
			uctx, _ := batchTestContext(t)
			uctx.BatchDispatch = func(ctx context.Context, _ string, input json.RawMessage) (*ContentBlock, error) {
				if strings.Contains(string(input), "fast") {
					return Result("ok"), nil
				}
				<-ctx.Done()
				return nil, ctx.Err()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			result := runPTCTest(t, ctx, DefaultBatchLimits, uctx, code)
			if !result.IsError || !strings.Contains(result.Text, "deadline exceeded") {
				t.Fatalf("cancellation lost: %+v", result)
			}
		})
	}
}

func TestBatchPTCScriptErrorsAndIsolation(t *testing.T) {
	uctx, _ := batchTestContext(t)
	for _, code := range []string{`throw new Error("boom")`, `await new Promise(()=>{})`, `const =`, `console.log(typeof tools.BatchTool); throw "boom"`, `Promise.reject(new Error("detached"))`, `tools.View({path:"missing"}).then(()=>{throw new Error("detached child")})`} {
		if result := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, code); !result.IsError {
			t.Fatalf("expected script failure: %+v", result)
		}
	}
	first := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, `globalThis.secret=42; console.log(typeof require,typeof process,typeof fetch,typeof setTimeout);`)
	second := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, `console.log(typeof secret);`)
	if first.IsError || first.Text != "undefined undefined undefined undefined" || second.IsError || second.Text != "undefined" {
		t.Fatalf("VM isolation failed: %+v / %+v", first, second)
	}
}

func TestBatchPTCStrictEnvelope(t *testing.T) {
	uctx, _ := batchTestContext(t)
	for _, input := range []string{`{"code":"console.log(1)","atomic":true}`, `{"code":""}`, `null`, `[]`, `{"operations":[]}`, `{"code":"1"}{"code":"2"}`} {
		result, err := NewBatchToolWithPTC(DefaultBatchLimits, true).Invoke(context.Background(), uctx, json.RawMessage(input))
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("accepted invalid envelope %s: %v / %+v", input, err, result)
		}
	}
}

func TestBatchPTCResultStorePaging(t *testing.T) {
	uctx, _ := batchTestContext(t)
	uctx.BatchDispatch = func(context.Context, string, json.RawMessage) (*ContentBlock, error) {
		return Result("你好世界末尾"), nil
	}
	limits := DefaultBatchLimits
	limits.MaxResultRunes = 2
	result := runPTCTest(t, context.Background(), limits, uctx, `
		const r = await tools.View({path:"x"});
		let text = r.content;
		for (let offset=r.next_offset; offset<r.total_runes;) {
		  const page = results.get(r.result_ref,{offset,limit:2}); text+=page.content; offset=page.next_offset;
		}
		console.log(r.truncated,text,results.get("missing").error,results.get(r.result_ref,{offset:-1}).error);`)
	if result.IsError || !strings.Contains(result.Text, "true 你好世界末尾 RESULT_NOT_FOUND INVALID_INPUT") {
		t.Fatalf("unexpected pages: %+v", result)
	}
}

func TestBatchPTCResultStoreBoundsAndIsolation(t *testing.T) {
	store := newPTCResultStore(3)
	result := store.put(Result("abcdef"))
	ref := result["result_ref"].(string)
	if result["content"] != "abc" || result["truncated"] != true {
		t.Fatalf("unexpected preview: %v", result)
	}
	if page := store.get(ref, 3, 1000); page["content"] != "def" || page["truncated"] != false {
		t.Fatalf("unexpected page: %v", page)
	}
	if got := newPTCResultStore(3).get(ref, 0, 0); got["error"] != "RESULT_NOT_FOUND" {
		t.Fatalf("cross-invocation reference allowed: %v", got)
	}
	large := store.put(Result(strings.Repeat("x", ptcMaxStoreBytes)))
	if large["ok"] != true || large["result_ref"] != nil || large["storage_error"] == nil || store.bytes != 6 {
		t.Fatalf("store cap lost: %v / %d", large, store.bytes)
	}
	failed := store.put(ErrorResult("denied"))
	if page := store.get(failed["result_ref"].(string), 0, 0); page["ok"] != false {
		t.Fatalf("error status lost: %v", page)
	}
}

func TestBatchPTCConsoleBoundedAndCycles(t *testing.T) {
	uctx, _ := batchTestContext(t)
	result := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, `console.log("你".repeat(100000)); console.log("not retained");`)
	if result.IsError || !utf8.ValidString(result.Text) || !strings.HasSuffix(result.Text, "[output truncated]") || utf8.RuneCountInString(result.Text) > ptcMaxOutputRunes+20 {
		t.Fatalf("console cap invalid: error=%v bytes=%d", result.IsError, len(result.Text))
	}
	cycle := runPTCTest(t, context.Background(), DefaultBatchLimits, uctx, `const a={}; a.a=a; console.log(a);`)
	if cycle.IsError || cycle.Text != "[object Object]" {
		t.Fatalf("cyclic log failed: %+v", cycle)
	}
}
