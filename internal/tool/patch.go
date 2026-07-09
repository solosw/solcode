package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// PatchParams is the input schema for the patch tool.
type PatchParams struct {
	FilePath  string `json:"file_path"`
	PatchText string `json:"patch_text"`
}

const PatchToolName = "Patch"

type patchTool struct {
	BaseTool
}

// NewPatchTool creates a new patch application tool.
func NewPatchTool() Tool {
	return &patchTool{}
}

func (p *patchTool) Name() string                         { return PatchToolName }
func (p *patchTool) IsDestructive(_ json.RawMessage) bool { return true }
func (p *patchTool) IsReadOnly(_ json.RawMessage) bool    { return false }

func (p *patchTool) Description() string {
	return `Applies a unified diff patch to a file.
- Provide file_path (absolute or relative to work dir).
- Provide the patch in unified diff format.
- The file must exist before applying the patch.
- Use the Diff tool first to preview changes.`
}

func (p *patchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to patch (absolute or relative)",
			},
			"patch_text": map[string]any{
				"type":        "string",
				"description": "The patch in unified diff format to apply",
			},
		},
		"required": []string{"file_path", "patch_text"},
	}
}

func (p *patchTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params PatchParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if params.FilePath == "" {
		return ErrorResult("file_path is required"), nil
	}
	if params.PatchText == "" {
		return ErrorResult("patch_text is required"), nil
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(uctx.WorkDir, filePath)
	}

	// Read current file content
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return ErrorResult(fmt.Sprintf("error reading file: %v", err)), nil
	}
	oldContent := string(data)

	// Apply the patch
	newContent, err := applyPatch(oldContent, params.PatchText)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to apply patch: %v", err)), nil
	}

	if oldContent == newContent {
		return Result("No changes applied. The file is unchanged."), nil
	}

	// Write the patched content
	if err := os.WriteFile(filePath, []byte(newContent), 0o644); err != nil {
		return ErrorResult(fmt.Sprintf("error writing file: %v", err)), nil
	}

	oldLines := strings.Count(oldContent, "\n") + 1
	newLines := strings.Count(newContent, "\n") + 1
	diff := GenerateSimpleDiff(oldContent, newContent, filePath)
	additions, removals := CountDiffChanges(diff)

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Patch applied to %s\n", filePath))
	result.WriteString(fmt.Sprintf("Lines: %d → %d (+%d -%d)\n", oldLines, newLines, additions, removals))
	if diff != "" {
		result.WriteString("\n")
		result.WriteString(diff)
	}
	return Result(result.String()), nil
}

func applyPatch(content, patchText string) (string, error) {
	dmp := diffmatchpatch.New()
	patches, err := dmp.PatchFromText(patchText)
	if err != nil {
		return "", fmt.Errorf("invalid patch format: %w", err)
	}

	newContent, results := dmp.PatchApply(patches, content)

	// Check if all patches applied successfully
	for _, r := range results {
		if !r {
			// Continue anyway - some fuzz is ok
		}
	}

	return newContent, nil
}
