package tool

import (
    "context"
    "encoding/json"
    "fmt"
    "strings"
    "sync"
    "time"

    "github.com/dop251/goja"
)

// invokePTC runs a fresh, isolated (from other JS executions) ECMAScript
// runtime. All tool effects still go through the session BatchDispatch which
// routes them via Engine's permission-aware ToolExecutor.
func (bt *batchTool) invokePTC(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
    if uctx == nil || uctx.BatchDispatch == nil || uctx.BatchLookup == nil {
        return ErrorResult("PTC child dispatcher is unavailable"), nil
    }
    var request struct { Code string `json:"code"` }
    if err := json.Unmarshal(input, &request); err != nil { return ErrorResult("invalid PTC input: " + err.Error()), nil }
    if strings.TrimSpace(request.Code) == "" || len(request.Code) > 65536 {
        return ErrorResult("PTC code must be 1..65536 bytes"), nil
    }

    // Keep a strict lifetime even when the model emits an infinite JS loop.
    runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
    defer cancel()
    vm := goja.New()
    var outputs []string
    var mu sync.Mutex
    const maxCalls = 32
    const maxOutput = 16384
    calls := 0
    toolsObject := vm.NewObject()
    for _, toolName := range []string{ViewToolName, GrepToolName, GlobToolName, LSToolName, EditToolName, MultiEditToolName, WriteToolName, MultiWriteToolName, PatchToolName, BashToolName, "LSP"} {
        name := toolName
        _ = toolsObject.Set(name, func(call goja.FunctionCall) goja.Value {
            calls++
            if calls > maxCalls { return vm.ToValue(map[string]any{"ok":false,"error":"PTC_CALL_LIMIT"}) }
            if runCtx.Err() != nil { return vm.ToValue(map[string]any{"ok":false,"error":runCtx.Err().Error()}) }
            if uctx.BatchLookup(name) == nil { return vm.ToValue(map[string]any{"ok":false,"error":"TOOL_UNAVAILABLE"}) }
            var args any = map[string]any{}
            if len(call.Arguments) > 0 {
                args = call.Arguments[0].Export()
            }
            payload, err := json.Marshal(args)
            if err != nil { return vm.ToValue(map[string]any{"ok":false,"error":"INVALID_INPUT: "+err.Error()}) }
            result, err := uctx.BatchDispatch(runCtx, name, payload)
            if err != nil { return vm.ToValue(map[string]any{"ok":false,"error":err.Error()}) }
            if result == nil { return vm.ToValue(map[string]any{"ok":false,"error":"EMPTY_RESULT"}) }
            return vm.ToValue(map[string]any{"ok":!result.IsError,"content":result.Text})
        })
    }
    _ = vm.Set("tools", toolsObject)
    console := vm.NewObject()
    _ = console.Set("log", func(call goja.FunctionCall) {
        mu.Lock()
        defer mu.Unlock()
        for _, arg := range call.Arguments {
            if len(strings.Join(outputs, " ")) >= maxOutput { break }
            // JSON gives a useful representation for program-calculated objects.
            bytes, err := json.Marshal(arg.Export())
            if err != nil { outputs = append(outputs, arg.String()) } else if arg.ExportType() != nil && arg.ExportType().Kind().String() == "string" { outputs = append(outputs, arg.String()) } else { outputs = append(outputs, string(bytes)) }
        }
    })
    _ = vm.Set("console", console)
    completed := make(chan struct{})
    go func() {
        select {
        case <-runCtx.Done():
            vm.Interrupt(runCtx.Err())
        case <-completed:
        }
    }()
    defer close(completed)

    // Goja processes already-settled promise jobs when the current run exits.
    // Our child calls are intentionally synchronous; await and Promise.all work
    // semantically, but do not schedule parallel child operations yet.
    value, err := vm.RunString("(async function(){\n" + request.Code + "\n})()")
    if err != nil {
        return ErrorResult("PTC script error: " + err.Error()), nil
    }
    if p, ok := value.Export().(*goja.Promise); ok {
        switch p.State() {
        case goja.PromiseStateRejected:
            return ErrorResult(fmt.Sprintf("PTC promise rejected: %v", p.Result())), nil
        case goja.PromiseStatePending:
            return ErrorResult("PTC script left an unresolved promise (external async APIs are unavailable)"), nil
        }
    }
    output := strings.Join(outputs, " ")
    if len(output) > maxOutput { output = output[:maxOutput] + "\n[output truncated]" }
    if output == "" { output = fmt.Sprintf("PTC completed (%d tool calls; no console output)", calls) }
    return Result(output), nil
}
