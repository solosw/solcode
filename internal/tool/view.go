package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ViewParams is the input schema for the view tool.
type ViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

const (
	ViewToolName     = "View"
	MaxReadSize      = 250 * 1024 // 250KB
	DefaultReadLimit = 2000
	MaxLineLength    = 2000
)

type viewTool struct {
	BaseTool
}

// NewViewTool creates a new file viewing tool.
func NewViewTool() Tool {
	return &viewTool{}
}

func (v *viewTool) Name() string                             { return ViewToolName }
func (v *viewTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (v *viewTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (v *viewTool) Description() string {
	return `Reads and displays file contents with line numbers.
Use this tool to examine source code, configuration files, or log files.
- Provide file_path (absolute or relative to work dir).
- Optionally specify offset (0-based line) and limit (default 2000).
- Maximum file size: 250KB.
- Lines longer than 2000 characters are truncated.
- Cannot display binary files or images.`
}

func (v *viewTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to read (absolute or relative)",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "The line number to start reading from (0-based)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "The number of lines to read (default 2000)",
			},
		},
		"required": []string{"file_path"},
	}
}

func (v *viewTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params ViewParams
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

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return SuggestSimilarFile(filePath), nil
		}
		return ErrorResult(fmt.Sprintf("error accessing file: %v", err)), nil
	}

	if fileInfo.IsDir() {
		return ErrorResult(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
	}

	if fileInfo.Size() > MaxReadSize {
		return ErrorResult(fmt.Sprintf("File is too large (%d bytes). Maximum size is %d bytes",
			fileInfo.Size(), MaxReadSize)), nil
	}

	if params.Limit <= 0 {
		params.Limit = DefaultReadLimit
	}

	if IsImagePath(filePath) {
		ext := strings.ToLower(filepath.Ext(filePath))
		return ErrorResult(fmt.Sprintf("This is an image file of type: %s. Use a different tool to process images.", ext)), nil
	}

	content, totalLines, err := ReadTextFile(filePath, params.Offset, params.Limit)
	if err != nil {
		return ErrorResult(fmt.Sprintf("error reading file: %v", err)), nil
	}

	var output strings.Builder
	output.WriteString("<file>\n")
	output.WriteString(AddLineNumbers(content, params.Offset+1))

	remaining := totalLines - (params.Offset + strings.Count(content, "\n") + 1)
	if remaining > 0 {
		output.WriteString(fmt.Sprintf("\n\n(File has %d more lines. Use 'offset' parameter to read beyond line %d)",
			remaining, params.Offset+strings.Count(content, "\n")+1))
	}
	output.WriteString("\n</file>")

	return Result(output.String()), nil
}

// ReadTextFile reads a file from offset for up to limit lines.
// Returns content, total line count, and error.
func ReadTextFile(filePath string, offset, limit int) (string, int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineCount := 0

	// Skip to offset
	for lineCount < offset && scanner.Scan() {
		lineCount++
	}

	var lines []string
	for scanner.Scan() && len(lines) < limit {
		lineCount++
		line := scanner.Text()
		if len(line) > MaxLineLength {
			line = line[:MaxLineLength] + "..."
		}
		lines = append(lines, line)
	}

	// Count remaining lines
	for scanner.Scan() {
		lineCount++
	}

	return strings.Join(lines, "\n"), lineCount, scanner.Err()
}

func AddLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	var result []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		lineNum := i + startLine
		result = append(result, fmt.Sprintf("%6d|%s", lineNum, line))
	}
	return strings.Join(result, "\n")
}

func IsImagePath(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".svg", ".webp", ".ico":
		return true
	}
	return false
}

func SuggestSimilarFile(filePath string) *ContentBlock {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ErrorResult(fmt.Sprintf("File not found: %s", filePath))
	}

	var suggestions []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(strings.ToLower(name), strings.ToLower(base)) ||
			strings.Contains(strings.ToLower(base), strings.ToLower(name)) {
			suggestions = append(suggestions, filepath.Join(dir, name))
			if len(suggestions) >= 3 {
				break
			}
		}
	}

	if len(suggestions) > 0 {
		return ErrorResult(fmt.Sprintf("File not found: %s\n\nDid you mean one of these?\n%s",
			filePath, strings.Join(suggestions, "\n")))
	}
	return ErrorResult(fmt.Sprintf("File not found: %s", filePath))
}
