package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

const (
	ptcMaxCalls       = 32
	ptcMaxCodeBytes   = 65536
	ptcMaxOutputRunes = 16384
)

type ptcCall struct {
	name     string
	input    json.RawMessage
	parallel bool
	resolve  func(interface{}) error
}

type ptcDispatchResult struct {
	content *ContentBlock
	err     error
}

// invokePTC owns the VM on this goroutine. Workers only dispatch tools; promise
// settlement and all JavaScript continuations run back on the VM owner.
func (bt *batchTool) invokePTC(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	if uctx == nil || uctx.BatchDispatch == nil || uctx.BatchLookup == nil {
		return ErrorResult("PTC child dispatcher is unavailable"), nil
	}
	var request struct {
		Code string `json:"code"`
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if !json.Valid(input) {
		return ErrorResult("invalid PTC input: expected a single JSON object"), nil
	}
	if err := decoder.Decode(&request); err != nil {
		return ErrorResult("invalid PTC input: " + err.Error()), nil
	}
	if strings.TrimSpace(request.Code) == "" || len(request.Code) > ptcMaxCodeBytes {
		return ErrorResult("PTC code must be 1..65536 bytes"), nil
	}

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	vm := goja.New()
	unhandled := make(map[*goja.Promise]bool)
	vm.SetPromiseRejectionTracker(func(p *goja.Promise, operation goja.PromiseRejectionOperation) {
		if operation == goja.PromiseRejectionReject {
			unhandled[p] = true
		} else {
			delete(unhandled, p)
		}
	})
	completed := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			vm.Interrupt(runCtx.Err())
		case <-completed:
		}
	}()
	defer close(completed)

	limits := bt.limits.withDefaults()
	store := newPTCResultStore(limits.MaxResultRunes)
	results := vm.NewObject()
	_ = results.Set("get", func(call goja.FunctionCall) goja.Value {
		var options struct {
			Offset int `json:"offset"`
			Limit  int `json:"limit"`
		}
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) {
			data, err := ptcJSONStringify(vm, call.Argument(1))
			if err != nil || json.Unmarshal(data, &options) != nil || string(data) == "null" {
				return vm.ToValue(ptcError("INVALID_INPUT: expected {offset, limit}"))
			}
		}
		return vm.ToValue(store.get(call.Argument(0).String(), options.Offset, options.Limit))
	})
	_ = vm.Set("results", results)

	var pending []ptcCall
	calls := 0
	tools := vm.NewObject()
	for _, toolName := range []string{ViewToolName, GrepToolName, GlobToolName, LSToolName, EditToolName, MultiEditToolName, WriteToolName, MultiWriteToolName, PatchToolName, BashToolName, "LSP"} {
		name := toolName
		_ = tools.Set(name, func(call goja.FunctionCall) goja.Value {
			promise, resolve, _ := vm.NewPromise()
			fail := func(message string) goja.Value {
				_ = resolve(ptcError(message))
				return vm.ToValue(promise)
			}
			calls++
			if calls > ptcMaxCalls {
				return fail("PTC_CALL_LIMIT")
			}
			if err := runCtx.Err(); err != nil {
				return fail(err.Error())
			}
			child := uctx.BatchLookup(name)
			if child == nil {
				return fail("TOOL_UNAVAILABLE")
			}
			if len(call.Arguments) != 1 {
				return fail("INVALID_INPUT: expected one tool input object")
			}
			payload, err := ptcJSONStringify(vm, call.Argument(0))
			if err != nil {
				return fail("INVALID_INPUT: " + err.Error())
			}
			if err := validateBatchChild(runCtx, child, payload); err != nil {
				return fail("INVALID_INPUT: " + err.Error())
			}
			pending = append(pending, ptcCall{
				name: name, input: payload, resolve: resolve,
				parallel: child.IsReadOnly(payload) && child.IsConcurrencySafe(payload),
			})
			return vm.ToValue(promise)
		})
	}
	_ = vm.Set("tools", tools)

	var output strings.Builder
	outputRunes := 0
	truncated := false
	console := vm.NewObject()
	_ = console.Set("log", func(call goja.FunctionCall) goja.Value {
		for _, arg := range call.Arguments {
			if outputRunes >= ptcMaxOutputRunes {
				truncated = true
				return goja.Undefined()
			}
			text := arg.String()
			if arg.ExportType() != reflect.TypeOf("") {
				if data, err := ptcJSONStringify(vm, arg); err == nil {
					text = string(data)
				}
			}
			if output.Len() > 0 {
				output.WriteByte(' ')
				outputRunes++
			}
			runes := []rune(text)
			remaining := ptcMaxOutputRunes - outputRunes
			if len(runes) > remaining {
				runes = runes[:remaining]
				truncated = true
			}
			output.WriteString(string(runes))
			outputRunes += len(runes)
		}
		return goja.Undefined()
	})
	_ = vm.Set("console", console)
	failure := func(message string) (*ContentBlock, error) {
		if output.Len() > 0 {
			message = output.String() + "\n" + message
		}
		message = ptcBoundedText(message, ptcMaxOutputRunes)
		return ErrorResult(message + "\nCompleted tool effects are not rolled back."), nil
	}

	value, err := vm.RunString("(async function(){\n" + request.Code + "\n})()")
	if err != nil {
		return failure("PTC script error: " + err.Error())
	}
	main, _ := value.Export().(*goja.Promise)
	for len(pending) > 0 {
		if err := runCtx.Err(); err != nil {
			return failure("PTC cancelled: " + err.Error())
		}
		if main != nil && main.State() == goja.PromiseStateRejected {
			break
		}
		// An unsafe call is a barrier: never overlap writes (or Bash) with
		// reads, even when they appear in the same Promise.all expression.
		n := 1
		if pending[0].parallel {
			for n < len(pending) && n < limits.MaxParallel && pending[n].parallel {
				n++
			}
		}
		wave := append([]ptcCall(nil), pending[:n]...)
		pending = pending[n:]
		finished := make([]ptcDispatchResult, n)
		var wg sync.WaitGroup
		for i := range wave {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				finished[i] = dispatchPTCCall(runCtx, uctx, wave[i])
			}(i)
		}
		wg.Wait()
		if err := runCtx.Err(); err != nil {
			return failure("PTC cancelled: " + err.Error())
		}
		// Resolve in enqueue order for deterministic JS continuations. The
		// continuations may enqueue more calls, but cannot touch worker state.
		for i, job := range wave {
			result := finished[i]
			var response map[string]any
			switch {
			case result.err != nil:
				response = ptcError(result.err.Error())
			case result.content == nil:
				response = ptcError("EMPTY_RESULT")
			case result.content.Type == "image":
				response = ptcError("UNSUPPORTED_OUTPUT: PTC accepts text results only")
			default:
				response = store.put(result.content)
			}
			if err := job.resolve(response); err != nil {
				return failure("PTC script error: " + err.Error())
			}
		}
	}
	if err := runCtx.Err(); err != nil {
		return failure("PTC cancelled: " + err.Error())
	}
	if main != nil {
		switch main.State() {
		case goja.PromiseStateRejected:
			return failure(fmt.Sprintf("PTC promise rejected: %v", main.Result()))
		case goja.PromiseStatePending:
			return failure("PTC script left an unresolved promise (external async APIs are unavailable)")
		}
	}
	for promise := range unhandled {
		return failure(fmt.Sprintf("PTC unhandled promise rejection: %v", promise.Result()))
	}
	if truncated {
		output.WriteString("\n[output truncated]")
	}
	if output.Len() == 0 {
		return Result(fmt.Sprintf("PTC completed (%d tool calls; no console output)", calls)), nil
	}
	return Result(output.String()), nil
}

func ptcBoundedText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "\n[output truncated]"
	}
	return text
}

func ptcError(message string) map[string]any {
	return map[string]any{"ok": false, "content": "", "error": message}
}

// JSON.stringify rejects cyclic values without recursively exporting them to Go.
func ptcJSONStringify(vm *goja.Runtime, value goja.Value) ([]byte, error) {
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	if stringify == nil {
		return nil, fmt.Errorf("JSON.stringify is unavailable")
	}
	encoded, err := stringify(goja.Undefined(), value)
	if err != nil {
		return nil, err
	}
	if goja.IsUndefined(encoded) {
		return nil, fmt.Errorf("value is not JSON serializable")
	}
	return []byte(encoded.String()), nil
}

func dispatchPTCCall(ctx context.Context, uctx *UseContext, call ptcCall) (result ptcDispatchResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = ptcDispatchResult{err: fmt.Errorf("PTC child panic: %v", recovered)}
		}
	}()
	if err := ctx.Err(); err != nil {
		return ptcDispatchResult{err: err}
	}
	result.content, result.err = uctx.BatchDispatch(ctx, call.name, call.input)
	return result
}
