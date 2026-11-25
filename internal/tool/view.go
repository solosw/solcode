package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// ViewParams is the input schema for the view tool.
type ViewParams struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

const (
	ViewToolName = "View"
	// MaxReadSize is retained for compatibility. View no longer rejects files solely
	// because stat size exceeds this; paging reads stream line windows instead.
	// It still bounds full-buffer paths (UTF-16 decode, legacy callers).
	MaxReadSize      = 250 * 1024 // 250KB
	DefaultReadLimit = 2000
	MaxLineLength    = 2000

	viewBinaryPeek     = 8 * 1024
	viewUTF16MaxBytes  = 1024 * 1024 // raw UTF-16 files are fully buffered up to this
	viewMaxSkipBytes   = 32 * 1024 * 1024
	viewScannerMaxLine = 1024 * 1024
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
	return `Reads text files with line numbers or returns images as multimodal content.
Use this tool to examine source code, configuration files, logs, screenshots, or photos.
- Provide path (absolute or relative to work dir).
- Optionally specify offset (0-based line) and limit (default 2000).
- Large files are read in pages; use offset/limit to continue — whole-file size is not a hard reject.
- Lines longer than 2000 characters are truncated.
- UTF-8 and UTF-16 text are supported; other binary files are not shown.
- Images up to 5MB are optimized (max edge 1280px) and returned as jpeg/png/gif/webp.
- Text offset/limit do not apply to images.`
}

func (v *viewTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
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
		"required": []string{"path"},
	}
}

func (v *viewTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params ViewParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	if params.Path == "" {
		return ErrorResult("path is required"), nil
	}

	filePath := ResolvePath(uctx, params.Path)
	if err := CheckAllowedPath(uctx, filePath); err != nil {
		return ErrorResult(err.Error()), nil
	}
	if params.Offset < 0 {
		params.Offset = 0
	}
	if params.Limit <= 0 {
		params.Limit = DefaultReadLimit
	}
	if IsImagePath(filePath) {
		return viewImage(filePath)
	}

	// ACP clients can expose unsaved editor buffers. Prefer that source whenever
	// the session advertised readTextFile instead of requiring a local stat.
	if uctx != nil && uctx.TextFileSystem != nil && uctx.TextFileSystem.CanReadTextFile() {
		data, err := ReadTextFileContent(ctx, uctx, filePath)
		if err != nil {
			return ErrorResult(fmt.Sprintf("error reading file: %v", err)), nil
		}
		return Result(formatLineWindow(splitFileLines(string(data)), params.Offset, params.Limit)), nil
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return SuggestSimilarFile(filePath), nil
		}
		return ErrorResult(fmt.Sprintf("error accessing file: %v", err)), nil
	}
	if fileInfo.IsDir() {
		return ErrorResult(fmt.Sprintf("%s is a directory, not a file — use the LS tool to list it, or read a specific file inside it", filePath)), nil
	}

	content, hasMore, err := ReadTextFile(filePath, params.Offset, params.Limit)
	if err != nil {
		return ErrorResult(fmt.Sprintf("error reading file: %v", err)), nil
	}
	return Result(formatViewContent(content, params.Offset, hasMore)), nil
}

// ReadTextFile reads up to limit lines starting at 0-based offset.
// hasMore is true when at least one line exists beyond the returned window.
// The reader stops after detecting hasMore and does not scan the rest of the file.
func ReadTextFile(filePath string, offset, limit int) (content string, hasMore bool, err error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}

	f, err := os.Open(filePath)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	peek := make([]byte, viewBinaryPeek)
	n, readErr := io.ReadFull(f, peek)
	peek = peek[:n]
	peekEOF := readErr != nil // short file / EOF

	if kind, ok := detectUTF16(peek); ok {
		rest, rerr := readAtMost(f, viewUTF16MaxBytes-len(peek))
		if rerr != nil {
			return "", false, rerr
		}
		all := append(peek, rest...)
		if !peekEOF && len(all) >= viewUTF16MaxBytes {
			return "", false, fmt.Errorf("UTF-16 file exceeds %d byte decode limit; convert to UTF-8 or read with another tool", viewUTF16MaxBytes)
		}
		text := decodeUTF16Bytes(all, kind)
		return pageLines(splitFileLines(text), offset, limit)
	}

	if bytes.IndexByte(peek, 0) >= 0 {
		return "", false, fmt.Errorf("binary file %s (NUL byte detected); not shown by View", filePath)
	}

	var src io.Reader
	if peekEOF {
		src = bytes.NewReader(peek)
	} else {
		src = io.MultiReader(bytes.NewReader(peek), f)
	}
	return readLineWindow(src, offset, limit)
}

func readLineWindow(src io.Reader, offset, limit int) (string, bool, error) {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), viewScannerMaxLine)

	var (
		collected []string
		lineNo    int
		hasMore   bool
		skipBytes int
	)
	for scanner.Scan() {
		lineNo++
		text := scanner.Text()
		if lineNo <= offset {
			skipBytes += len(text) + 1
			if skipBytes > viewMaxSkipBytes {
				return "", false, fmt.Errorf("refusing to skip more than %d bytes to reach offset %d", viewMaxSkipBytes, offset)
			}
			continue
		}
		if len(collected) < limit {
			collected = append(collected, truncateViewLine(text))
			continue
		}
		hasMore = true
		break
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	if lineNo == 0 {
		return "", false, nil
	}
	if len(collected) == 0 {
		return "", false, nil
	}
	return strings.Join(collected, "\n"), hasMore, nil
}

func pageLines(lines []string, offset, limit int) (string, bool, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	window := make([]string, end-offset)
	for i, line := range lines[offset:end] {
		window[i] = truncateViewLine(line)
	}
	hasMore := end < len(lines)
	if len(window) == 0 {
		return "", hasMore, nil
	}
	return strings.Join(window, "\n"), hasMore, nil
}

func formatViewContent(content string, offset int, hasMore bool) string {
	if content == "" {
		if offset > 0 {
			return fmt.Sprintf("(offset %d is past EOF or the file is empty)", offset)
		}
		return "(empty file)"
	}
	var b strings.Builder
	b.WriteString("<file>\n")
	b.WriteString(AddLineNumbers(content, offset+1))
	if hasMore {
		shown := strings.Count(content, "\n") + 1
		next := offset + shown
		b.WriteString(fmt.Sprintf("\n\n[more lines below; pass offset=%d to continue]", next))
	}
	b.WriteString("\n</file>")
	return b.String()
}

func formatLineWindow(lines []string, offset, limit int) string {
	content, hasMore, _ := pageLines(lines, offset, limit)
	return formatViewContent(content, offset, hasMore)
}

func splitFileLines(content string) []string {
	if content == "" {
		return nil
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func truncateViewLine(line string) string {
	line = strings.TrimSuffix(line, "\r")
	if len(line) > MaxLineLength {
		return line[:MaxLineLength] + "... [truncated]"
	}
	return line
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
	return supportedImageExtensions[strings.ToLower(filepath.Ext(filePath))]
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

// utf16Kind selects little- or big-endian UTF-16 decoding.
type utf16Kind int

const (
	utf16LE utf16Kind = iota
	utf16BE
)

func detectUTF16(peek []byte) (utf16Kind, bool) {
	if len(peek) >= 2 {
		if peek[0] == 0xFF && peek[1] == 0xFE {
			return utf16LE, true
		}
		if peek[0] == 0xFE && peek[1] == 0xFF {
			return utf16BE, true
		}
	}
	// BOM-less heuristic: ASCII text as UTF-16LE has NULs on odd bytes.
	if kind, ok := detectUTF16NoBOM(peek); ok {
		return kind, true
	}
	return 0, false
}

func detectUTF16NoBOM(peek []byte) (utf16Kind, bool) {
	if len(peek) < 16 {
		return 0, false
	}
	n := len(peek)
	if n > 64 {
		n = 64
	}
	evenNUL, oddNUL := 0, 0
	for i := 0; i < n; i++ {
		if peek[i] != 0 {
			continue
		}
		if i%2 == 0 {
			evenNUL++
		} else {
			oddNUL++
		}
	}
	// Prefer the side that looks like UTF-16 padding for ASCII.
	if oddNUL >= n/4 && evenNUL <= n/16 {
		return utf16LE, true
	}
	if evenNUL >= n/4 && oddNUL <= n/16 {
		return utf16BE, true
	}
	return 0, false
}

func decodeUTF16Bytes(data []byte, kind utf16Kind) string {
	if len(data) >= 2 {
		switch {
		case data[0] == 0xFF && data[1] == 0xFE && kind == utf16LE:
			data = data[2:]
		case data[0] == 0xFE && data[1] == 0xFF && kind == utf16BE:
			data = data[2:]
		}
	}
	if len(data)%2 == 1 {
		data = data[:len(data)-1]
	}
	u16s := make([]uint16, len(data)/2)
	for i := 0; i < len(u16s); i++ {
		if kind == utf16BE {
			u16s[i] = binary.BigEndian.Uint16(data[i*2:])
		} else {
			u16s[i] = binary.LittleEndian.Uint16(data[i*2:])
		}
	}
	return string(utf16.Decode(u16s))
}

func readAtMost(r io.Reader, max int) ([]byte, error) {
	if max <= 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	_, err := io.Copy(&buf, io.LimitReader(r, int64(max+1)))
	if err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > max {
		return b[:max], nil
	}
	return b, nil
}
