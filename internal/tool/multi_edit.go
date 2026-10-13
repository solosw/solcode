package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// MultiEditParams applies ordered replacements atomically per file by default.
// Atomic opts into a transaction across every file.
type MultiEditParams struct {
	Edits  []EditParams `json:"edits"`
	Atomic bool         `json:"atomic,omitempty"`
}

const MultiEditToolName = "MultiEdit"

type multiEditTool struct{ BaseTool }

// NewMultiEditTool creates a batch exact-replacement tool.
func NewMultiEditTool() Tool { return &multiEditTool{} }

func (t *multiEditTool) Name() string                         { return MultiEditToolName }
func (t *multiEditTool) IsDestructive(_ json.RawMessage) bool { return true }
func (t *multiEditTool) IsReadOnly(_ json.RawMessage) bool    { return false }

func (t *multiEditTool) Description() string {
	return `Applies multiple exact text replacements with per-file atomicity by default.
Use this when one file has multiple changes or several files need targeted edits.

Input: edits is an ordered list of path, old_string, new_string, and optional desc.
Each old_string must occur exactly once in the file state produced by preceding edits.
By default, a failed file is left unchanged while independent files are still applied.
Edits within a file remain ordered and all-or-nothing. Do not resend successful files.
Set atomic=true for all-or-nothing validation and commit across every file.`
}

func (t *multiEditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"atomic": map[string]any{"type": "boolean", "description": "Default false: per-file atomicity, independent files continue. True: transaction across all files."},
			"edits": map[string]any{
				"type":        "array",
				"description": "Ordered exact replacements to apply together.",
				"minItems":    1,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":       map[string]any{"type": "string"},
						"old_string": map[string]any{"type": "string"},
						"new_string": map[string]any{"type": "string"},
						"desc":       map[string]any{"type": "string"},
					},
					"required": []string{"path", "old_string", "new_string"},
				},
			},
		},
		"required": []string{"edits"},
	}
}

func (t *multiEditTool) ValidateInput(_ context.Context, input json.RawMessage) error {
	var params MultiEditParams
	if err := json.Unmarshal(input, &params); err != nil {
		return err
	}
	if len(params.Edits) == 0 {
		return fmt.Errorf("at least one edit is required")
	}
	for i, edit := range params.Edits {
		if strings.TrimSpace(edit.Path) == "" {
			return fmt.Errorf("edits[%d].path is required", i)
		}
	}
	return nil
}

type stagedEditFile struct {
	path    string
	before  string
	after   string
	existed bool
	descs   []string
}

func (t *multiEditTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params MultiEditParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	if err := t.ValidateInput(ctx, input); err != nil {
		return ErrorResult(err.Error()), nil
	}

	if params.Atomic {
		return t.invokeAtomic(ctx, uctx, params.Edits)
	}
	// Group aliases resolving to the same file, preserving first-file order and
	// per-file edit order. A failed file cannot taint unrelated files.
	type group struct {
		path    string
		edits   []EditParams
		indexes []int
	}
	groups := []*group{}
	byPath := map[string]*group{}
	for i, edit := range params.Edits {
		path := ResolvePath(uctx, edit.Path)
		g := byPath[path]
		if g == nil {
			g = &group{path: path}
			byPath[path] = g
			groups = append(groups, g)
		}
		g.edits = append(g.edits, edit)
		g.indexes = append(g.indexes, i)
	}
	var messages []string
	applied, failed := 0, 0
	for _, g := range groups {
		if ctx.Err() != nil {
			failed++
			messages = append(messages, fmt.Sprintf("FAILED %s (edits %v): %v", g.path, g.indexes, ctx.Err()))
			continue
		}
		out, err := t.invokeAtomic(ctx, uctx, g.edits)
		if err != nil {
			return nil, err
		}
		if out.IsError {
			failed++
			messages = append(messages, fmt.Sprintf("FAILED %s (edits %v; file unchanged unless rollback failed): %s", g.path, g.indexes, out.Text))
		} else {
			applied += len(g.edits)
			_, details, _ := strings.Cut(out.Text, "\n")
			messages = append(messages, details)
		}
	}
	text := fmt.Sprintf("Applied %d/%d edits; %d file(s) failed. Successful files remain changed; retry only failed files.\n%s", applied, len(params.Edits), failed, strings.Join(messages, "\n"))
	if failed > 0 {
		return ErrorResult(text), nil
	}
	return Result(fmt.Sprintf("Applied %d edits across %d files.\n%s", applied, len(groups), strings.Join(messages, "\n"))), nil
}

func (t *multiEditTool) invokeAtomic(ctx context.Context, uctx *UseContext, edits []EditParams) (*ContentBlock, error) {

	files := make([]*stagedEditFile, 0, len(edits))
	byPath := make(map[string]*stagedEditFile, len(edits))
	for index, edit := range edits {
		if _, errText := validateChangeDescription(edit.Desc); errText != "" {
			return ErrorResult(fmt.Sprintf("edits[%d]: %s", index, errText)), nil
		}
		path := ResolvePath(uctx, edit.Path)
		if err := CheckAllowedPath(uctx, path); err != nil {
			return ErrorResult(fmt.Sprintf("edits[%d]: %v", index, err)), nil
		}
		file := byPath[path]
		if file == nil {
			data, err := ReadTextFileContent(ctx, uctx, path)
			existed := err == nil
			if err != nil && !os.IsNotExist(err) {
				return ErrorResult(fmt.Sprintf("edits[%d]: read %s: %v", index, path, err)), nil
			}
			file = &stagedEditFile{path: path, before: string(data), after: string(data), existed: existed}
			byPath[path] = file
			files = append(files, file)
		}
		if err := applyStagedEdit(file, edit); err != nil {
			return ErrorResult(fmt.Sprintf("edits[%d] (%s): %v", index, edit.Path, err)), nil
		}
		if desc, _ := validateChangeDescription(edit.Desc); desc != "" {
			file.descs = append(file.descs, desc)
		}
	}

	if err := commitStagedEdits(ctx, uctx, files); err != nil {
		return ErrorResult(err.Error()), nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Applied %d edits across %d files.\n", len(edits), len(files)))
	for _, file := range files {
		desc := strings.Join(file.descs, "; ")
		if desc == "" {
			desc = "batch edit"
		}
		recordFileChange(ctx, uctx, MultiEditToolName, file.path, desc, file.before, file.after)
		additions, removals := CountDiffChanges(GenerateSimpleDiff(file.before, file.after, file.path))
		fmt.Fprintf(&result, "- %s (+%d -%d)\n", file.path, additions, removals)
	}
	return Result(strings.TrimSpace(result.String())), nil
}

func applyStagedEdit(file *stagedEditFile, edit EditParams) error {
	if edit.OldString == "" {
		if file.existed || file.after != "" {
			return fmt.Errorf("old_string may be empty only when creating a new empty file")
		}
		file.after = edit.NewString
		return nil
	}
	index := strings.Index(file.after, edit.OldString)
	if index < 0 {
		return fmt.Errorf("old_string not found; it must match exactly")
	}
	if index != strings.LastIndex(file.after, edit.OldString) {
		return fmt.Errorf("old_string appears multiple times; include more context for a unique match")
	}
	file.after = file.after[:index] + edit.NewString + file.after[index+len(edit.OldString):]
	return nil
}

func commitStagedEdits(ctx context.Context, uctx *UseContext, files []*stagedEditFile) error {
	committed := make([]*stagedEditFile, 0, len(files))
	for _, file := range files {
		if err := WriteTextFileContent(ctx, uctx, file.path, file.after); err != nil {
			// A backend may fail after partially writing the current file. Restore
			// it too, using a bounded cleanup context independent of cancellation.
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			rollbackErr := rollbackStagedEdits(rollbackCtx, uctx, append(committed, file))
			cancel()
			if rollbackErr != nil {
				return fmt.Errorf("write %s: %w; rollback failed: %v", file.path, err, rollbackErr)
			}
			return fmt.Errorf("write %s: %w (rolled back)", file.path, err)
		}
		committed = append(committed, file)
	}
	return nil
}

func rollbackStagedEdits(ctx context.Context, uctx *UseContext, files []*stagedEditFile) error {
	var failures []error
	for index := len(files) - 1; index >= 0; index-- {
		file := files[index]
		if file.existed {
			if err := WriteTextFileContent(ctx, uctx, file.path, file.before); err != nil {
				failures = append(failures, fmt.Errorf("restore %s: %w", file.path, err))
			}
			continue
		}
		// ACP exposes no delete-text-file operation. Never delete a local file
		// in its place or claim that rollback fully restored the original state.
		if uctx == nil || uctx.TextFileSystem == nil || !uctx.TextFileSystem.CanWriteTextFile() {
			if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
				failures = append(failures, fmt.Errorf("remove %s: %w", file.path, err))
			}
		} else {
			failures = append(failures, fmt.Errorf("cannot remove new client-backed file %s: delete capability unavailable", file.path))
		}
	}
	return errors.Join(failures...)
}
