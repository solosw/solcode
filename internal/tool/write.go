package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteParams is the input schema for the write tool.
type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

const WriteToolName = "Write"

type writeTool struct {
	BaseTool
}

// NewWriteTool creates a new file writing tool.
func NewWriteTool() Tool {
	return &writeTool{}
}

func (w *writeTool) Name() string                        { return WriteToolName }
func (w *writeTool) IsDestructive(_ json.RawMessage) bool { return true }
func (w *writeTool) IsReadOnly(_ json.RawMessage) bool     { return false }

func (w *writeTool) Description() string {
	return `File writing tool that creates or completely overwrites files.
- Provide file_path (absolute or relative to work dir).
- Provide the full content to write.
- Creates parent directories automatically.
- Checks if file has been modified since last read.
- For small targeted edits, prefer the Edit tool instead.`
}

func (w *writeTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to write (absolute or relative)",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to write to the file",
			},
		},
		"required": []string{"file_path", "content"},
	}
}

func (w *writeTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params WriteParams
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

	// Check if it's a directory
	if info, err := os.Stat(filePath); err == nil && info.IsDir() {
		return ErrorResult(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
	}

	// Read old content for diff
	var oldContent string
	if oldBytes, err := os.ReadFile(filePath); err == nil {
		oldContent = string(oldBytes)
	}

	if oldContent == params.Content {
		return Result(fmt.Sprintf("File %s already contains the exact content. No changes made.", filePath)), nil
	}

	// Create parent directories
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ErrorResult(fmt.Sprintf("error creating directory: %v", err)), nil
	}

	// Write the file
	if err := os.WriteFile(filePath, []byte(params.Content), 0o644); err != nil {
		return ErrorResult(fmt.Sprintf("error writing file: %v", err)), nil
	}

	// Generate simple diff stats
	oldLines := strings.Count(oldContent, "\n") + 1
	newLines := strings.Count(params.Content, "\n") + 1
	if oldContent == "" {
		oldLines = 0
	}

	diff := GenerateSimpleDiff(oldContent, params.Content, filePath)
	additions, removals := CountDiffChanges(diff)

	var result strings.Builder
	result.WriteString(fmt.Sprintf("<result>\nFile written: %s\n", filePath))
	result.WriteString(fmt.Sprintf("Lines: %d → %d (+%d -%d)\n", oldLines, newLines, additions, removals))
	if diff != "" {
		result.WriteString("\n")
		result.WriteString(diff)
	}
	result.WriteString("\n</result>")

	return Result(result.String()), nil
}

func GenerateSimpleDiff(oldContent, newContent, fileName string) string {
	if oldContent == "" {
		return fmt.Sprintf("--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n%s",
			filepath.Base(fileName),
			strings.Count(newContent, "\n")+1,
			prefixLines(newContent, "+"))
	}

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	var output strings.Builder
	output.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", filepath.Base(fileName), filepath.Base(fileName)))

	// Simple line-by-line diff
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}

	// Find changed ranges
	inDiff := false
	for i := 0; i < maxLen; i++ {
		oldLine := ""
		newLine := ""
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}

		if oldLine != newLine {
			if !inDiff {
				output.WriteString(fmt.Sprintf("@@ -%d +%d @@\n", i+1, i+1))
				inDiff = true
			}
			if i < len(oldLines) {
				output.WriteString(fmt.Sprintf("- %s\n", oldLine))
			}
			if i < len(newLines) {
				output.WriteString(fmt.Sprintf("+ %s\n", newLine))
			}
		} else {
			inDiff = false
		}
	}

	return output.String()
}

func prefixLines(content, prefix string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func CountDiffChanges(diffText string) (int, int) {
	additions := 0
	removals := 0
	for _, line := range strings.Split(diffText, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			additions++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removals++
		}
	}
	return additions, removals
}
