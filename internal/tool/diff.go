package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// DiffParams is the input schema for the diff tool.
type DiffParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

const DiffToolName = "Diff"

type diffTool struct {
	BaseTool
}

// NewDiffTool creates a new diff generation tool.
func NewDiffTool() Tool {
	return &diffTool{}
}

func (d *diffTool) Name() string                        { return DiffToolName }
func (d *diffTool) IsReadOnly(_ json.RawMessage) bool    { return true }
func (d *diffTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (d *diffTool) Description() string {
	return `Generates a unified diff between the file on disk and the provided content.
Use this to preview changes before applying them with Write or Edit.
- Provide file_path (absolute or relative to work dir).
- Provide the proposed new content.
- Returns a unified diff.`
}

func (d *diffTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to diff (absolute or relative)",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The proposed new content to diff against",
			},
		},
		"required": []string{"file_path", "content"},
	}
}

func (d *diffTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params DiffParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if params.FilePath == "" {
		return ErrorResult("file_path is required"), nil
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(uctx.WorkDir, filePath)
	}

	var oldContent string
	if data, err := os.ReadFile(filePath); err == nil {
		oldContent = string(data)
	}

	diff := generateDMPDiff(oldContent, params.Content, filePath)

	if diff == "" {
		return Result("No differences found."), nil
	}

	return Result("<diff>\n" + diff + "\n</diff>"), nil
}

func generateDMPDiff(oldContent, newContent, fileName string) string {
	if oldContent == newContent {
		return ""
	}

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldContent, newContent, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", filepath.Base(fileName), filepath.Base(fileName)))

	patches := dmp.PatchMake(oldContent, diffs)
	patchText := dmp.PatchToText(patches)
	buf.WriteString(patchText)

	return buf.String()
}
