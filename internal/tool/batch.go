package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BatchToolName is the public coding-tool entrypoint. The underlying tools are
// still run through Engine's normal executor with their own permissions/hooks.
const BatchToolName = "BatchTool"

// BatchLimits bounds a single batch. Per-operation and aggregate text limits
// are configurable separately, as are operation count and read parallelism.
type BatchLimits struct {
	// MaxOperations caps how many operations one batch may contain.
	MaxOperations int
	// MaxParallel caps how many read-only operations run concurrently in one
	// wave. Writes always run sequentially.
	MaxParallel int
	// MaxResultRunes truncates each operation's output text.
	MaxResultRunes int
	// MaxTotalResultRunes caps combined output/error text for the entire batch.
	MaxTotalResultRunes int
	// MaxReadRetries is the bounded retry count for read-only operations that
	// fail with a transient error. Writes are never retried.
	MaxReadRetries int
}

// DefaultBatchLimits keep each operation useful while bounding the aggregate
// text budget independently of the operation count.
var DefaultBatchLimits = BatchLimits{
	MaxOperations:       12,
	MaxParallel:         4,
	MaxResultRunes:      18000,
	MaxTotalResultRunes: 24000,
	MaxReadRetries:      1,
}

func (l BatchLimits) withDefaults() BatchLimits {
	if l.MaxOperations <= 0 {
		l.MaxOperations = DefaultBatchLimits.MaxOperations
	}
	if l.MaxParallel <= 0 {
		l.MaxParallel = DefaultBatchLimits.MaxParallel
	}
	if l.MaxResultRunes <= 0 {
		l.MaxResultRunes = DefaultBatchLimits.MaxResultRunes
	}
	if l.MaxTotalResultRunes <= 0 {
		l.MaxTotalResultRunes = DefaultBatchLimits.MaxTotalResultRunes
	}
	// MaxReadRetries may legitimately be 0 (disable retry); only negative is
	// treated as unset.
	if l.MaxReadRetries < 0 {
		l.MaxReadRetries = DefaultBatchLimits.MaxReadRetries
	}
	return l
}

type batchOperation struct {
	ID        string          `json:"id"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input"`
	DependsOn []string        `json:"depends_on,omitempty"`
}

type batchRequest struct {
	FailurePolicy string           `json:"failure_policy,omitempty"`
	OutputMode    string           `json:"output_mode,omitempty"`
	Atomic        bool             `json:"atomic,omitempty"`
	Operations    []batchOperation `json:"operations"`
}

type batchOutput struct {
	Content        string `json:"content"`
	Truncated      bool   `json:"truncated,omitempty"`
	Summarized     bool   `json:"summarized,omitempty"`
	FullOutputPath string `json:"full_output_path,omitempty"`
}

type batchItem struct {
	ID        string       `json:"id"`
	Tool      string       `json:"tool"`
	Status    string       `json:"status"`
	Reverted  bool         `json:"reverted,omitempty"`
	Output    *batchOutput `json:"output,omitempty"`
	Error     *batchError  `json:"error,omitempty"`
	BlockedBy []string     `json:"blocked_by,omitempty"`
}
type batchError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	Retryable      bool   `json:"retryable"`
	Truncated      bool   `json:"truncated,omitempty"`
	FullOutputPath string `json:"full_output_path,omitempty"`
}
type batchSummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
	Cancelled int `json:"cancelled"`
	// Reverted counts operations that ran successfully but whose effects were
	// undone by an atomic rollback. Their effects no longer exist.
	Reverted int `json:"reverted"`
}
type batchResponse struct {
	BatchID            string       `json:"batch_id"`
	Status             string       `json:"status"`
	AtomicStatus       string       `json:"atomic_status,omitempty"`
	Summary            batchSummary `json:"summary"`
	Results            []batchItem  `json:"results"`
	OutputStorageError string       `json:"output_storage_error,omitempty"`
}

// fileSnapshot is the pre-transaction content of one atomic target.
type fileSnapshot struct {
	path    string
	existed bool
	content *string
}

var batchCounter int64

func nextBatchID() string {
	return fmt.Sprintf("batch_%03d", atomic.AddInt64(&batchCounter, 1))
}

type batchTool struct {
	ptcEnabled bool
	BaseTool
	limits BatchLimits
}

func NewBatchTool() Tool { return &batchTool{limits: DefaultBatchLimits} }

// NewBatchToolWithPTC selects one wire schema and execution mode for this tool instance.
func NewBatchToolWithPTC(limits BatchLimits, enabled bool) Tool {
	return &batchTool{limits: limits.withDefaults(), ptcEnabled: enabled}
}

// NewBatchToolWithLimits builds the batch tool with caller-supplied limits.
func NewBatchToolWithLimits(limits BatchLimits) Tool {
	return &batchTool{limits: limits.withDefaults()}
}

func (*batchTool) Name() string { return BatchToolName }
func (bt *batchTool) Description() string {
	if bt.ptcEnabled { return "Run a bounded JavaScript program with tools.View/Grep/Glob/LS/Edit/MultiEdit/Write/MultiWrite/Patch/Bash/LSP. Use tools.Name({args}) to call a tool; results are {ok, content}. Intermediate results remain inside the program; console.log prints only required findings. async/await works, but child tool calls currently execute synchronously (Promise.all is not concurrent). No Node APIs or direct filesystem/network access. At most 32 child calls. Use only trusted tools and print concise output." }
	return fmt.Sprintf("Run 1-%d coding operations using the tool-specific input schemas below. IDs are optional (default op_1, op_2, ...); use explicit IDs for depends_on. Dependencies only order execution: they do NOT substitute results into later inputs. Only batch edits whose exact old_string is already known from a prior read. Independent safe reads run concurrently, mutations sequentially. In non-atomic batches, successful edits stay applied after unrelated failures, even when a MultiEdit partially fails. Default output_mode=compact omits repeated file diffs; read/search/Bash outputs remain useful within per-item and total text budgets. Truncated or summarized outputs include full_output_path; read it with View using offset/limit. output_mode=full keeps diffs but still obeys budgets. atomic=true attempts to roll back all file operations on any failure; supports Edit/MultiEdit/Write/MultiWrite/Patch, including repeated edits to the same file. Inspect atomic_status and reverted: after rolled_back, reapply the corrected transaction; after rollback_failed/unknown, read current targets first. Permissions and hooks still apply to each child.", bt.limits.MaxOperations)
}

// Classify read batches for plan-mode permissions. Mutation permissions are
// still checked separately by the real child executor, never by this wrapper.
func (bt *batchTool) IsReadOnly(input json.RawMessage) bool {
	if bt.ptcEnabled { return false }
	var req batchRequest
	if json.Unmarshal(input, &req) != nil || req.Atomic || len(req.Operations) == 0 {
		return false
	}
	for _, op := range req.Operations {
		switch op.Tool {
		case ViewToolName, GrepToolName, GlobToolName, LSToolName, "LSP":
		default:
			return false
		}
	}
	return true
}
func (*batchTool) IsConcurrencySafe(json.RawMessage) bool { return false }
func (bt *batchTool) InputSchema() map[string]any {
	if bt.ptcEnabled { return map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{"code":map[string]any{"type":"string","minLength":1,"maxLength":65536}},"required":[]string{"code"}} }
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"failure_policy": map[string]any{"type": "string", "enum": []string{"continue_independent"}},
			"output_mode":    map[string]any{"type": "string", "enum": []string{"compact", "full"}, "description": "Default compact: summaries for single-file edits; full retains diffs. Both modes obey output budgets."},
			"atomic":         map[string]any{"type": "boolean", "description": "All-or-nothing transaction for file edits only; default false."},
			"operations":     map[string]any{"type": "array", "minItems": 1, "maxItems": bt.limits.MaxOperations, "items": batchOperationSchema()},
		}, "required": []string{"operations"},
	}
}

func (bt *batchTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	if bt.ptcEnabled { return bt.invokePTC(ctx, uctx, input) }
	if uctx == nil || uctx.BatchDispatch == nil || uctx.BatchLookup == nil {
		return ErrorResult("batch execution is not configured"), nil
	}
	if !json.Valid(input) {
		return ErrorResult("invalid batch request: expected a single valid JSON object"), nil
	}
	var req batchRequest
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return ErrorResult("invalid batch request: " + err.Error()), nil
	}
	if req.OutputMode != "" && req.OutputMode != "compact" && req.OutputMode != "full" {
		return ErrorResult("output_mode must be compact or full"), nil
	}
	if req.FailurePolicy != "" && req.FailurePolicy != "continue_independent" {
		return ErrorResult("unsupported failure_policy"), nil
	}
	if len(req.Operations) == 0 || len(req.Operations) > bt.limits.MaxOperations {
		return ErrorResult(fmt.Sprintf("operations must contain 1-%d entries", bt.limits.MaxOperations)), nil
	}
	// Reserve explicit IDs before generating defaults to avoid collisions.
	used := map[string]bool{}
	for _, op := range req.Operations {
		if op.ID != "" {
			used[op.ID] = true
		}
	}
	for i := range req.Operations {
		if req.Operations[i].ID == "" {
			id := fmt.Sprintf("op_%d", i+1)
			for used[id] {
				id += "_"
			}
			req.Operations[i].ID = id
			used[id] = true
		}
	}
	ids := map[string]int{}
	for i, op := range req.Operations {
		if strings.TrimSpace(op.ID) == "" || strings.TrimSpace(op.Tool) == "" || len(op.Input) == 0 {
			return ErrorResult("each operation requires id, tool, and input"), nil
		}
		if _, ok := ids[op.ID]; ok {
			return ErrorResult("duplicate operation id: " + op.ID), nil
		}
		ids[op.ID] = i
	}
	// Reject cyclic graphs before any side-effecting operation executes.
	visiting := make([]uint8, len(req.Operations))
	var visit func(int) bool
	visit = func(i int) bool {
		if visiting[i] == 1 {
			return false
		}
		if visiting[i] == 2 {
			return true
		}
		visiting[i] = 1
		for _, dep := range req.Operations[i].DependsOn {
			j, ok := ids[dep]
			if !ok {
				continue // unknown dependencies reported below
			}
			if !visit(j) {
				return false
			}
		}
		visiting[i] = 2
		return true
	}
	for i := range req.Operations {
		if !visit(i) {
			return ErrorResult("batch has circular dependencies"), nil
		}
	}
	for i, op := range req.Operations {
		for _, dep := range op.DependsOn {
			j, ok := ids[dep]
			if !ok {
				return ErrorResult("unknown dependency: " + dep), nil
			}
			if j == i {
				return ErrorResult("self dependency: " + dep), nil
			}
		}
	}

	if req.Atomic {
		return bt.invokeAtomic(ctx, uctx, req, ids)
	}
	return bt.invokeIndependent(ctx, uctx, req)
}

// invokeIndependent runs the default continue_independent policy: a DAG whose
// independent nodes run concurrently (read-only waves) and whose failed
// dependencies skip their downstream nodes. Successful results are always kept.
func (bt *batchTool) invokeIndependent(ctx context.Context, uctx *UseContext, req batchRequest) (*ContentBlock, error) {
	ids := map[string]int{}
	for i, op := range req.Operations {
		ids[op.ID] = i
	}
	// Kahn-style scheduler: complete each ready wave before advancing.
	result := make([]batchItem, len(req.Operations))
	done := make([]bool, len(req.Operations))
	completed := 0
	for completed < len(req.Operations) {
		ready := []int{}
		skippedThisPass := 0
		for i, op := range req.Operations {
			if done[i] {
				continue
			}
			blocked := []string{}
			waiting := false
			for _, dep := range op.DependsOn {
				j := ids[dep]
				if !done[j] {
					waiting = true
					break
				}
				if result[j].Status != "success" {
					blocked = append(blocked, dep)
				}
			}
			if waiting {
				continue
			}
			if len(blocked) > 0 {
				result[i] = batchItem{ID: op.ID, Tool: op.Tool, Status: "skipped", BlockedBy: blocked}
				done[i] = true
				completed++
				skippedThisPass++
			} else {
				ready = append(ready, i)
			}
		}
		if len(ready) == 0 {
			if completed == len(req.Operations) {
				break
			}
			// Skipped nodes may unblock their own downstream dependents on the
			// next scheduler pass; only an iteration with no progress is a cycle.
			if skippedThisPass > 0 {
				continue
			}
			return ErrorResult("batch has circular dependencies"), nil
		}
		// A wave runs concurrently only if *all* calls are safe read-only
		// operations. Otherwise execute sequentially to protect file mutations.
		parallel := len(ready) > 1 && bt.limits.MaxParallel > 1
		for _, i := range ready {
			op := req.Operations[i]
			t := uctx.BatchLookup(op.Tool)
			if t == nil || !BatchWrapped(op.Tool) || !t.IsReadOnly(op.Input) || !t.IsConcurrencySafe(op.Input) {
				parallel = false
				break
			}
		}
		run := func(i int) { result[i] = bt.runOperation(ctx, uctx, req.Operations[i]) }
		if parallel {
			var wg sync.WaitGroup
			semaphore := make(chan struct{}, bt.limits.MaxParallel)
			for _, i := range ready {
				semaphore <- struct{}{}
				wg.Add(1)
				go func(i int) { defer wg.Done(); defer func() { <-semaphore }(); run(i) }(i)
			}
			wg.Wait()
		} else {
			for _, i := range ready {
				run(i)
			}
		}
		for _, i := range ready {
			done[i] = true
			completed++
		}
	}
	response := batchResponse{BatchID: nextBatchID(), Results: result}
	bt.finish(&response)
	return bt.encode(ctx, uctx, response, req.OutputMode)
}

// runOperation executes one child call through the executor, applying the
// bounded read-only retry policy and structured error classification.
func (bt *batchTool) runOperation(ctx context.Context, uctx *UseContext, op batchOperation) batchItem {
	item := batchItem{ID: op.ID, Tool: op.Tool, Status: "success"}
	if !BatchWrapped(op.Tool) {
		item.Status = "error"
		item.Error = &batchError{Code: "UNSUPPORTED_TOOL", Message: "tool not supported in batch: " + op.Tool + "; use exact tool names from the schema"}
		return item
	}
	t := uctx.BatchLookup(op.Tool)
	if t == nil {
		item.Status = "error"
		item.Error = &batchError{Code: "TOOL_UNAVAILABLE", Message: "tool unavailable: " + op.Tool}
		return item
	}
	if err := validateBatchChild(ctx, t, op.Input); err != nil {
		item.Status = "error"
		item.Error = &batchError{Code: "INVALID_INPUT", Message: fmt.Sprintf("%s input: %v", op.Tool, err)}
		return item
	}
	readOnly := t != nil && t.IsReadOnly(op.Input) && t.IsConcurrencySafe(op.Input)

	output, err := uctx.BatchDispatch(ctx, op.Tool, op.Input)
	// Bounded, idempotent retry: only for read-only calls whose failure looks
	// transient. Writes and deterministic errors are never retried.
	if err == nil && output != nil && output.IsError && readOnly && bt.limits.MaxReadRetries > 0 && isTransientError(output.Text) {
		for attempt := 0; attempt < bt.limits.MaxReadRetries; attempt++ {
			if ctx.Err() != nil {
				break
			}
			output, err = uctx.BatchDispatch(ctx, op.Tool, op.Input)
			if err != nil || output == nil || !output.IsError || !isTransientError(output.Text) {
				break
			}
		}
	}

	if err != nil {
		item.Status = "error"
		code, retryable := classifyChildError(err.Error())
		item.Error = &batchError{Code: code, Message: err.Error(), Retryable: retryable}
	}
	if err == nil && output == nil {
		item.Status = "error"
		item.Error = &batchError{Code: "NO_RESULT", Message: "tool produced no result"}
	}
	if output != nil {
		if output.IsError {
			item.Status = "error"
			code, retryable := classifyChildError(output.Text)
			message := output.Text
			if code == "EDIT_MISMATCH" {
				message += "\nRead the current failed target with View and use unique exact text. Retry only failed files/replacements; successful files remain changed unless atomic rollback is reported."
			}
			item.Error = &batchError{Code: code, Message: message, Retryable: retryable}
		}
		if output.Type == "image" {
			item.Status = "error"
			item.Error = &batchError{Code: "UNSUPPORTED_OUTPUT", Message: "batch does not embed image payloads; call this tool directly"}
		}
		if item.Status == "success" {
			item.Output = &batchOutput{Content: output.Text}
		}
	}
	if ctx.Err() != nil {
		item.Status = "cancelled"
		item.Error = &batchError{Code: "CANCELLED", Message: ctx.Err().Error()}
	}
	return item
}

// invokeAtomic applies the file edits as a single transaction. Every target is
// snapshotted first, edits apply serially in operation order, and any failure
// rolls every target back. It never claims a rollback succeeded when it did not.
func (bt *batchTool) invokeAtomic(ctx context.Context, uctx *UseContext, req batchRequest, ids map[string]int) (*ContentBlock, error) {
	// 1. Validate: file-edit tools only, dependencies must point backward (so
	// application order is deterministic), and no two operations may share a
	// target path (overlap is ambiguous to roll back).
	seen := map[string]bool{}
	paths := make([]string, 0, len(req.Operations))
	for i, op := range req.Operations {
		if !CheckpointableFileTool(op.Tool) {
			return ErrorResult("atomic mode supports only file edits (Edit, MultiEdit, Write, MultiWrite, Patch); " + op.Tool + " is not allowed"), nil
		}
		if err := validateBatchChild(ctx, uctx.BatchLookup(op.Tool), op.Input); err != nil {
			return ErrorResult(fmt.Sprintf("atomic operation %s: %v", op.ID, err)), nil
		}
		for _, dep := range op.DependsOn {
			if ids[dep] >= i {
				return ErrorResult("atomic mode requires depends_on to reference earlier operations: " + dep), nil
			}
		}
		opPaths := PathsForCheckpoint(op.Tool, op.Input)
		if len(opPaths) == 0 {
			return ErrorResult("atomic operation has no resolvable target path: " + op.ID), nil
		}
		for _, p := range opPaths {
			abs := ResolvePath(uctx, p)
			if strings.TrimSpace(abs) == "" {
				return ErrorResult("atomic operation has an empty target path: " + op.ID), nil
			}
			if seen[abs] {
				continue // Snapshot once; ordered edits may legitimately share a file.
			}
			seen[abs] = true
			paths = append(paths, abs)
		}
	}

	// 2. Snapshot every target (content nil means the file did not exist).
	snapshots := make([]fileSnapshot, 0, len(paths))
	for _, abs := range paths {
		snap := fileSnapshot{path: abs}
		data, err := ReadTextFileContent(ctx, uctx, abs)
		if err != nil {
			if !os.IsNotExist(err) {
				return ErrorResult("atomic snapshot failed for " + abs + ": " + err.Error()), nil
			}
		} else {
			text := string(data)
			snap.existed = true
			snap.content = &text
		}
		snapshots = append(snapshots, snap)
	}

	// 3. Apply serially. Stop at the first failure.
	n := len(req.Operations)
	results := make([]batchItem, n)
	failedAt := -1
	for i, op := range req.Operations {
		item := bt.runOperation(ctx, uctx, op)
		results[i] = item
		if item.Status != "success" {
			failedAt = i
			break
		}
	}

	response := batchResponse{BatchID: nextBatchID(), Results: results}
	if failedAt < 0 {
		response.AtomicStatus = "committed"
	} else {
		for j := failedAt + 1; j < n; j++ {
			results[j] = batchItem{ID: req.Operations[j].ID, Tool: req.Operations[j].Tool, Status: "skipped", BlockedBy: []string{req.Operations[failedAt].ID}}
		}
		rollbackState := rollbackSnapshots(ctx, uctx, snapshots)
		response.Results = results
		response.AtomicStatus = rollbackState
		// Operations that ran before the failure were applied and are only
		// durable if the rollback itself succeeded. Reflect that honestly:
		// mark them reverted, or downgrade them when rollback failed.
		if rollbackState != "committed" {
			for i := 0; i < failedAt; i++ {
				if results[i].Status != "success" {
					continue
				}
				switch rollbackState {
				case "rolled_back":
					results[i].Reverted = true
				case "rollback_failed", "unknown":
					results[i].Status = "error"
					results[i].Output = nil
					results[i].Error = &batchError{
						Code:    "ROLLBACK_FAILED",
						Message: "operation applied but rollback could not be confirmed (" + rollbackState + "); file state is " + rollbackState,
					}
				}
			}
		}
	}
	bt.finish(&response)
	// A rolled-back batch applied nothing durable, so report it as failed.
	if response.AtomicStatus != "committed" {
		response.Status = "failed"
	}
	return bt.encode(ctx, uctx, response, req.OutputMode)
}

// rollbackSnapshots shares MultiEdit's bounded, cancellation-independent
// cleanup. ACP lacks deletion, so failed restoration must never delete a local
// file in place of a client-owned file or claim a successful rollback.
func rollbackSnapshots(ctx context.Context, uctx *UseContext, snapshots []fileSnapshot) string {
	files := make([]*stagedEditFile, 0, len(snapshots))
	for _, snap := range snapshots {
		file := &stagedEditFile{path: snap.path, existed: snap.existed}
		if snap.content != nil {
			file.before = *snap.content
		}
		files = append(files, file)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := rollbackStagedEdits(cleanupCtx, uctx, files); err != nil {
		if cleanupCtx.Err() != nil {
			return "unknown"
		}
		return "rollback_failed"
	}
	return "rolled_back"
}

// finish computes the summary and overall batch status from the results.
func (bt *batchTool) finish(response *batchResponse) {
	response.Summary = batchSummary{Total: len(response.Results)}
	for _, r := range response.Results {
		if r.Reverted {
			// An operation whose effect was undone by rollback is not a durable
			// success; count it separately so the summary never overstates what
			// actually persisted.
			response.Summary.Reverted++
			continue
		}
		switch r.Status {
		case "success":
			response.Summary.Succeeded++
		case "error":
			response.Summary.Failed++
		case "skipped":
			response.Summary.Skipped++
		case "cancelled":
			response.Summary.Cancelled++
		}
	}
	switch {
	case response.Summary.Succeeded == len(response.Results):
		response.Status = "success"
	case response.Summary.Succeeded > 0:
		response.Status = "partial_success"
	default:
		response.Status = "failed"
	}
}

func (bt *batchTool) encode(ctx context.Context, uctx *UseContext, response batchResponse, mode string) (*ContentBlock, error) {
	bt.compactOutputs(ctx, uctx, &response, mode)
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	// Partial failure is an informative tool result, not a failed model-level call.
	return Result(string(encoded)), nil
}

// classifyChildError maps a child failure message to a structured error code.
// Only clearly transient read failures are marked retryable; file-not-found,
// validation, permission and conflict errors never are.
func classifyChildError(text string) (string, bool) {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "old_string") && (strings.Contains(lower, "not found") || strings.Contains(lower, "multiple times") || strings.Contains(lower, "match exactly")):
		return "EDIT_MISMATCH", false
	case strings.Contains(lower, "no such file") || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist"):
		return "FILE_NOT_FOUND", false
	case strings.Contains(lower, "permission") || strings.Contains(lower, "denied") || strings.Contains(lower, "not allowed"):
		return "PERMISSION_DENIED", false
	case strings.Contains(lower, "conflict") || strings.Contains(lower, "hash"):
		return "CONFLICT", false
	case strings.Contains(lower, "invalid") || strings.Contains(lower, "required") || strings.Contains(lower, "unknown parameter"):
		return "INVALID_INPUT", false
	case isTransientError(text):
		return "TRANSIENT", true
	default:
		return "TOOL_ERROR", false
	}
}

func isTransientError(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"timeout", "timed out", "deadline exceeded", "temporarily unavailable",
		"connection reset", "connection refused", "try again", "resource busy",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// BatchWrapped identifies the coding tools offered through BatchTool instead of
// individually in the model tool schema.
func BatchWrapped(name string) bool {
	switch name {
	case ViewToolName, GrepToolName, GlobToolName, LSToolName, EditToolName, MultiEditToolName, WriteToolName, MultiWriteToolName, PatchToolName, BashToolName, "LSP":
		return true
	default:
		return false
	}
}
