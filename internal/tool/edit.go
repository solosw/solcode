package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EditParams is the input schema for the edit tool.
type EditParams struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
	Desc      string `json:"desc,omitempty"`
}

const EditToolName = "Edit"

type editTool struct {
	BaseTool
}

// NewEditTool creates a new file editing tool.
func NewEditTool() Tool {
	return &editTool{}
}

func (e *editTool) Name() string                         { return EditToolName }
func (e *editTool) IsDestructive(_ json.RawMessage) bool { return true }
func (e *editTool) IsReadOnly(_ json.RawMessage) bool    { return false }

func (e *editTool) ValidateInput(_ context.Context, input json.RawMessage) error {
	var params EditParams
	if err := json.Unmarshal(input, &params); err != nil {
		return err
	}
	_, errText := validateChangeDescription(params.Desc)
	if errText != "" {
		return fmt.Errorf("%s", errText)
	}
	return nil
}

func (e *editTool) Description() string {
	return `Edits files by replacing text with exact string matching.
Use this for small, precise changes. For larger edits, use the Write tool.

Requirements:
1. file_path: Absolute or relative path to the file
2. old_string: The text to replace (must be UNIQUE in the file, must match exactly)
3. new_string: The replacement text
4. desc (optional): A change description of up to 30 Chinese characters

Special cases:
- To create a new file: provide file_path and new_string, leave old_string empty
- To delete content: provide file_path and old_string, leave new_string empty

CRITICAL:
- The old_string must uniquely identify the instance to change
- Include 3-5 lines of context before and after
- Match whitespace and indentation exactly
- Single instance per call (use multiple calls for multiple edits)`
}

func (e *editTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The absolute path to the file to modify",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "The text to replace",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The text to replace it with",
			},
			"desc": map[string]any{
				"type":        "string",
				"description": "Optional description of this change (up to 30 Chinese characters)",
				"maxLength":   maxChangeDescriptionRunes,
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}
}

func (e *editTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params EditParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if params.FilePath == "" {
		return ErrorResult("file_path is required"), nil
	}
	desc, errText := validateChangeDescription(params.Desc)
	if errText != "" {
		return ErrorResult(errText), nil
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(uctx.WorkDir, filePath)
	}

	// Case 1: Create new file
	if params.OldString == "" {
		return e.createFile(ctx, uctx, filePath, params.NewString, desc)
	}

	// Case 2: Delete content
	if params.NewString == "" {
		return e.deleteContent(ctx, uctx, filePath, params.OldString, desc)
	}

	// Case 3: Replace content
	return e.replaceContent(ctx, uctx, filePath, params.OldString, params.NewString, desc)
}

func (e *editTool) createFile(ctx context.Context, uctx *UseContext, filePath, content, desc string) (*ContentBlock, error) {
	if info, err := os.Stat(filePath); err == nil {
		if info.IsDir() {
			return ErrorResult(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
		}
		return ErrorResult(fmt.Sprintf("file already exists: %s (use Edit with old_string or use Write to overwrite)", filePath)), nil
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ErrorResult(fmt.Sprintf("error creating parent directories: %v", err)), nil
	}

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return ErrorResult(fmt.Sprintf("error writing file: %v", err)), nil
	}
	recordFileChange(ctx, uctx, EditToolName, filePath, desc, "", content)

	diff := GenerateSimpleDiff("", content, filePath)
	additions, removals := CountDiffChanges(diff)
	var result strings.Builder
	result.WriteString(fmt.Sprintf("File created: %s\n", filePath))
	result.WriteString(fmt.Sprintf("Lines changed: +%d -%d\n", additions, removals))
	if diff != "" {
		result.WriteString("\n")
		result.WriteString(diff)
	}
	return Result(result.String()), nil
}

func (e *editTool) deleteContent(ctx context.Context, uctx *UseContext, filePath, oldString, desc string) (*ContentBlock, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
	}
	if info.IsDir() {
		return ErrorResult(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("error reading file: %v", err)), nil
	}
	content := string(data)

	index := strings.Index(content, oldString)
	if index == -1 {
		return ErrorResult("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks."), nil
	}

	lastIndex := strings.LastIndex(content, oldString)
	if index != lastIndex {
		return ErrorResult("old_string appears multiple times in the file. Please provide more context to ensure a unique match."), nil
	}

	newContent := content[:index] + content[index+len(oldString):]

	if err := os.WriteFile(filePath, []byte(newContent), 0o644); err != nil {
		return ErrorResult(fmt.Sprintf("error writing file: %v", err)), nil
	}
	recordFileChange(ctx, uctx, EditToolName, filePath, desc, content, newContent)

	diff := GenerateSimpleDiff(content, newContent, filePath)
	additions, removals := CountDiffChanges(diff)
	var result strings.Builder
	result.WriteString(fmt.Sprintf("Content deleted from file: %s\n", filePath))
	result.WriteString(fmt.Sprintf("Lines changed: +%d -%d\n", additions, removals))
	if diff != "" {
		result.WriteString("\n")
		result.WriteString(diff)
	}
	return Result(result.String()), nil
}

func (e *editTool) replaceContent(ctx context.Context, uctx *UseContext, filePath, oldString, newString, desc string) (*ContentBlock, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
	}
	if info.IsDir() {
		return ErrorResult(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("error reading file: %v", err)), nil
	}
	content := string(data)

	index := strings.Index(content, oldString)
	if index == -1 {
		return ErrorResult("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks."), nil
	}

	lastIndex := strings.LastIndex(content, oldString)
	if index != lastIndex {
		return ErrorResult("old_string appears multiple times in the file. Please provide more context to ensure a unique match."), nil
	}

	newContent := content[:index] + newString + content[index+len(oldString):]

	if content == newContent {
		return Result("New content is the same as old content. No changes made."), nil
	}

	if err := os.WriteFile(filePath, []byte(newContent), 0o644); err != nil {
		return ErrorResult(fmt.Sprintf("error writing file: %v", err)), nil
	}
	recordFileChange(ctx, uctx, EditToolName, filePath, desc, content, newContent)

	// Generate diff for display
	diff := GenerateSimpleDiff(content, newContent, filePath)
	additions, removals := CountDiffChanges(diff)

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Content replaced in file: %s\n", filePath))
	result.WriteString(fmt.Sprintf("Lines changed: +%d -%d\n", additions, removals))
	if diff != "" {
		result.WriteString("\n")
		result.WriteString(diff)
	}

	return Result(result.String()), nil
}

func CountLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
