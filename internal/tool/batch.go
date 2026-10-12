package tool

import (
 "context"
 "encoding/json"
 "fmt"
 "strings"
 "sync"
)

// BatchToolName is the public coding-tool entrypoint. The underlying tools are
// still run through Engine's normal executor with their own permissions/hooks.
const BatchToolName = "BatchTool"

const maxBatchOperations = 12
const maxBatchResultRunes = 18000

type batchOperation struct {
 ID string `json:"id"`
 Tool string `json:"tool"`
 Input json.RawMessage `json:"input"`
 DependsOn []string `json:"depends_on,omitempty"`
}

type batchRequest struct {
 FailurePolicy string `json:"failure_policy,omitempty"`
 Atomic bool `json:"atomic,omitempty"`
 Operations []batchOperation `json:"operations"`
}

type batchItem struct {
 ID string `json:"id"`
 Tool string `json:"tool"`
 Status string `json:"status"`
 Output string `json:"output,omitempty"`
 Error *batchError `json:"error,omitempty"`
 BlockedBy []string `json:"blocked_by,omitempty"`
 Truncated bool `json:"truncated,omitempty"`
}
type batchError struct {
 Code string `json:"code"`
 Message string `json:"message"`
 Retryable bool `json:"retryable"`
}
type batchSummary struct {
 Total int `json:"total"`
 Succeeded int `json:"succeeded"`
 Failed int `json:"failed"`
 Skipped int `json:"skipped"`
 Cancelled int `json:"cancelled"`
}
type batchResponse struct {
 Status string `json:"status"`
 Summary batchSummary `json:"summary"`
 Results []batchItem `json:"results"`
}

type batchTool struct{ BaseTool }

func NewBatchTool() Tool { return &batchTool{} }
func (*batchTool) Name() string { return BatchToolName }
func (*batchTool) Description() string {
 return "Batch up to 12 coding operations in one model request. Examples: View input {path,offset,limit}; Grep {pattern,path}; Glob {pattern,path}; LS {path}; Edit {path,old_string,new_string}; Write {path,content}; Bash {command}. Operation inputs must match the underlying tool schema. Use depends_on IDs for known dependencies, not for independent reads. Results keep successes and report independent errors separately. Read-only operations may run in parallel, writes are sequential. Each operation retains its normal permissions and hooks. atomic=true is unsupported."
}
func (*batchTool) IsReadOnly(json.RawMessage) bool { return false }
func (*batchTool) IsConcurrencySafe(json.RawMessage) bool { return false }
func (*batchTool) InputSchema() map[string]any {
 return map[string]any{
 "type":"object",
 "properties":map[string]any{
   "failure_policy":map[string]any{"type":"string","enum":[]string{"continue_independent"},"description":"Independent operations continue after a failure (the default)."},
   "atomic":map[string]any{"type":"boolean","description":"Reserved for file-edit transactions; currently rejected if true."},
   "operations":map[string]any{"type":"array","minItems":1,"maxItems":maxBatchOperations,"items":map[string]any{
     "type":"object","properties":map[string]any{
       "id":map[string]any{"type":"string","description":"Unique identifier within the batch."},
       "tool":map[string]any{"type":"string","description":"Underlying tool name, e.g. View, Grep, Glob, LS, Edit, Write, Patch, Bash."},
       "input":map[string]any{"type":"object","description":"Arguments matching the underlying tool's schema."},
       "depends_on":map[string]any{"type":"array","items":map[string]any{"type":"string"}},
     },"required":[]string{"id","tool","input"},
   }},
 },"required":[]string{"operations"},
 }
}

func (*batchTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock,error) {
 if uctx==nil || uctx.BatchDispatch==nil || uctx.BatchLookup==nil {
  return ErrorResult("batch execution is not configured"),nil
 }
 var req batchRequest
 if err:=json.Unmarshal(input,&req); err!=nil {return ErrorResult("invalid batch request: "+err.Error()),nil}
 if req.Atomic {return ErrorResult("atomic file-edit transactions are not supported yet; run non-atomic edits or use existing checkpoint tools"),nil}
 if req.FailurePolicy!="" && req.FailurePolicy!="continue_independent" {return ErrorResult("unsupported failure_policy"),nil}
 if len(req.Operations)==0 || len(req.Operations)>maxBatchOperations {return ErrorResult(fmt.Sprintf("operations must contain 1-%d entries",maxBatchOperations)),nil}
 ids:=map[string]int{}
 for i,op:=range req.Operations {
  if strings.TrimSpace(op.ID)=="" || strings.TrimSpace(op.Tool)=="" || len(op.Input)==0 {return ErrorResult("each operation requires id, tool, and input"),nil}
  if _,ok:=ids[op.ID];ok {return ErrorResult("duplicate operation id: "+op.ID),nil}
  ids[op.ID]=i
  if !BatchWrapped(op.Tool) {return ErrorResult("tool not supported in batch: "+op.Tool),nil}
  if uctx.BatchLookup(op.Tool)==nil {return ErrorResult("tool unavailable: "+op.Tool),nil}
 }
 // Reject cyclic graphs before any side-effecting operation executes.
 visiting:=make([]uint8,len(req.Operations))
 var visit func(int) bool
 visit=func(i int) bool {
  if visiting[i]==1 {return false}
  if visiting[i]==2 {return true}
  visiting[i]=1
  for _,dep:=range req.Operations[i].DependsOn {
   j,ok:=ids[dep]; if !ok {continue} // unknown dependencies reported below
   if !visit(j) {return false}
  }
  visiting[i]=2
  return true
 }
 for i:=range req.Operations {
  if !visit(i) {return ErrorResult("batch has circular dependencies"),nil}
 }
 for i,op:=range req.Operations {
  for _,dep:=range op.DependsOn {
   j,ok:=ids[dep]; if !ok {return ErrorResult("unknown dependency: "+dep),nil}
   if j==i {return ErrorResult("self dependency: "+dep),nil}
  }
 }
 // Kahn-style scheduler: complete each ready wave before advancing.
 result:=make([]batchItem,len(req.Operations))
 done:=make([]bool,len(req.Operations))
 completed:=0
 for completed<len(req.Operations) {
  ready:=[]int{}
  skippedThisPass:=0
  for i,op:=range req.Operations {
   if done[i] {continue}
   blocked:=[]string{}
   waiting:=false
   for _,dep:=range op.DependsOn {
    j:=ids[dep]
    if !done[j] {waiting=true;break}
    if result[j].Status!="success" {blocked=append(blocked,dep)}
   }
   if waiting {continue}
   if len(blocked)>0 {
    result[i]=batchItem{ID:op.ID,Tool:op.Tool,Status:"skipped",BlockedBy:blocked}
    done[i]=true;completed++;skippedThisPass++
   } else {ready=append(ready,i)}
  }
  if len(ready)==0 {
   if completed==len(req.Operations) {break}
   // Skipped nodes may unblock their own downstream dependents on the
   // next scheduler pass; only an iteration with no progress is a cycle.
   if skippedThisPass>0 {continue}
   return ErrorResult("batch has circular dependencies"),nil
  }
  // A wave runs concurrently only if *all* calls are safe read-only
  // operations. Otherwise execute sequentially to protect file mutations.
  parallel:=len(ready)>1 && len(ready)<=4
  for _,i:=range ready {
   op:=req.Operations[i]
   t:=uctx.BatchLookup(op.Tool)
   if !t.IsReadOnly(op.Input)||!t.IsConcurrencySafe(op.Input) {parallel=false;break}
  }
  run:=func(i int) {
   op:=req.Operations[i]
   item:=batchItem{ID:op.ID,Tool:op.Tool,Status:"success"}
   output,err:=uctx.BatchDispatch(ctx,op.Tool,op.Input)
   if err!=nil {item.Status="error";item.Error=&batchError{Code:"TOOL_ERROR",Message:err.Error()}}
   if err==nil && output==nil {item.Status="error";item.Error=&batchError{Code:"NO_RESULT",Message:"tool produced no result"}}
   if output!=nil {
    if output.IsError {item.Status="error";item.Error=&batchError{Code:"TOOL_ERROR",Message:output.Text}}
    if output.Type=="image" {item.Status="error";item.Error=&batchError{Code:"UNSUPPORTED_OUTPUT",Message:"batch does not embed image payloads; call this tool directly"}}
    item.Output=output.Text
    if len([]rune(item.Output))>maxBatchResultRunes {item.Output=string([]rune(item.Output)[:maxBatchResultRunes]);item.Truncated=true}
   }
   if ctx.Err()!=nil {item.Status="cancelled";item.Error=&batchError{Code:"CANCELLED",Message:ctx.Err().Error()}}
   result[i]=item
  }
  if parallel {
   var wg sync.WaitGroup
   for _,i:=range ready {wg.Add(1);go func(i int){defer wg.Done();run(i)}(i)}
   wg.Wait()
  } else {for _,i:=range ready {run(i)}}
  for _,i:=range ready {done[i]=true;completed++}
 }
 response:=batchResponse{Results:result,Summary:batchSummary{Total:len(result)}}
 for _,r:=range result {switch r.Status {
 case "success":response.Summary.Succeeded++
 case "error":response.Summary.Failed++
 case "skipped":response.Summary.Skipped++
 case "cancelled":response.Summary.Cancelled++
 }}
 switch {
 case response.Summary.Succeeded==len(result):response.Status="success"
 case response.Summary.Succeeded>0:response.Status="partial_success"
 default:response.Status="failed"
 }
 encoded,err:=json.Marshal(response)
 if err!=nil {return nil,err}
 // Partial failure is an informative tool result, not a failed model-level call.
 return Result(string(encoded)),nil
}

// BatchWrapped identifies the coding tools offered through BatchTool instead of
// individually in the model tool schema.
func BatchWrapped(name string) bool {
 switch name {
 case ViewToolName,GrepToolName,GlobToolName,LSToolName,EditToolName,MultiEditToolName,WriteToolName,MultiWriteToolName,PatchToolName,BashToolName,"LSP":
  return true
 default:return false
 }
}
