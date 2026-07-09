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
	if oldContent == newContent {
		return ""
	}

	dmp := diffmatchpatch.New()
	text1, text2, lineArray := dmp.DiffLinesToChars(oldContent, newContent)
	diffs := dmp.DiffMain(text1, text2, false)
	diffs = dmp.DiffCleanupSemantic(diffs)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)
	lines := diffLinesFromDiffs(diffs)
	if len(lines) == 0 {
		return ""
	}

	var output strings.Builder
	if oldContent == "" {
		output.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n", filepath.Base(fileName)))
	} else {
		output.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", filepath.Base(fileName), filepath.Base(fileName)))
	}
	writeUnifiedHunks(&output, lines, 3)
	return output.String()
}

type unifiedDiffLine struct {
	op    byte
	text  string
	oldNo int
	newNo int
}

func diffLinesFromDiffs(diffs []diffmatchpatch.Diff) []unifiedDiffLine {
	lines := make([]unifiedDiffLine, 0)
	oldNo, newNo := 1, 1
	for _, diff := range diffs {
		var op byte
		switch diff.Type {
		case diffmatchpatch.DiffDelete:
			op = '-'
		case diffmatchpatch.DiffInsert:
			op = '+'
		default:
			op = ' '
		}
		for _, line := range splitDiffTextLines(diff.Text) {
			lines = append(lines, unifiedDiffLine{op: op, text: line, oldNo: oldNo, newNo: newNo})
			if op != '+' {
				oldNo++
			}
			if op != '-' {
				newNo++
			}
		}
	}
	return lines
}

func splitDiffTextLines(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.SplitAfter(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSuffix(part, "\n")
		part = strings.TrimSuffix(part, "\r")
		lines = append(lines, part)
	}
	return lines
}

func writeUnifiedHunks(output *strings.Builder, lines []unifiedDiffLine, context int) {
	for search := 0; search < len(lines); {
		change := -1
		for i := search; i < len(lines); i++ {
			if lines[i].op != ' ' {
				change = i
				break
			}
		}
		if change == -1 {
			return
		}
		hunkStart := maxInt(change-context, 0)
		hunkEnd := change
		trailingContext := 0
		for i := change; i < len(lines); i++ {
			if lines[i].op == ' ' {
				trailingContext++
				if trailingContext > context {
					break
				}
			} else {
				trailingContext = 0
			}
			hunkEnd = i + 1
		}
		writeUnifiedHunk(output, lines[hunkStart:hunkEnd])
		search = hunkEnd
	}
}

func writeUnifiedHunk(output *strings.Builder, hunk []unifiedDiffLine) {
	if len(hunk) == 0 {
		return
	}
	oldStart, newStart, oldCount, newCount := hunkRange(hunk)
	output.WriteString(fmt.Sprintf("@@ -%s +%s @@\n", formatUnifiedRange(oldStart, oldCount), formatUnifiedRange(newStart, newCount)))
	for _, line := range hunk {
		output.WriteByte(line.op)
		output.WriteString(line.text)
		output.WriteByte('\n')
	}
}

func hunkRange(hunk []unifiedDiffLine) (oldStart, newStart, oldCount, newCount int) {
	oldStart, newStart = -1, -1
	for _, line := range hunk {
		if line.op != '+' {
			if oldStart == -1 {
				oldStart = line.oldNo
			}
			oldCount++
		}
		if line.op != '-' {
			if newStart == -1 {
				newStart = line.newNo
			}
			newCount++
		}
	}
	if oldStart == -1 {
		oldStart = hunk[0].oldNo - 1
		if oldStart < 0 {
			oldStart = 0
		}
	}
	if newStart == -1 {
		newStart = hunk[0].newNo - 1
		if newStart < 0 {
			newStart = 0
		}
	}
	return oldStart, newStart, oldCount, newCount
}

func formatUnifiedRange(start, count int) string {
	return fmt.Sprintf("%d,%d", start, count)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
