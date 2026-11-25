package acp

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/tool"
)

// maxACPInlineImageBytes caps base64 image payloads on tool_call_update so
// clients can preview without multi‑MB notifications.
const maxACPInlineImageBytes = 1_500_000

// toolOutputContent builds ACP tool_call_update content.
// Always includes the text output (path caption). When the caption includes a
// path: line for a saved/viewed image, also attaches resource_link + optional
// inline image, and locations so clients can open the file.
func toolOutputContent(name, output string, isError bool) ([]ToolCallContent, []ToolCallLocation) {
	_ = name
	textBlock := ToolCallContent{
		Type:    "content",
		Content: &ContentBlock{Type: "text", Text: output},
	}
	if isError {
		return []ToolCallContent{textBlock}, nil
	}
	path := extractImagePathFromToolOutput(output)
	if path == "" {
		return []ToolCallContent{textBlock}, nil
	}
	content := []ToolCallContent{textBlock}
	mime := imageMIMEFromExt(filepath.Ext(path))
	uri := pathToFileURI(path)
	content = append(content, ToolCallContent{
		Type: "content",
		Content: &ContentBlock{
			Type:     "resource_link",
			URI:      uri,
			Name:     filepath.Base(path),
			MimeType: mime,
			Text:     path,
		},
	})
	if data, ok := readSmallImageBase64(path, maxACPInlineImageBytes); ok {
		content = append(content, ToolCallContent{
			Type: "content",
			Content: &ContentBlock{
				Type:     "image",
				MimeType: mime,
				Data:     data,
				Name:     filepath.Base(path),
			},
		})
	}
	return content, []ToolCallLocation{{Path: path}}
}

func extractImagePathFromToolOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		var found string
		switch {
		case strings.HasPrefix(lower, "path:"):
			found = strings.TrimSpace(line[len("path:"):])
		case strings.HasPrefix(lower, "path :"):
			found = strings.TrimSpace(line[len("path :"):])
		}
		if found == "" {
			continue
		}
		found = strings.Trim(found, `"'`)
		if found != "" {
			return found
		}
	}
	return ""
}

func pathToFileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.ToSlash(abs)
	// Windows: C:/foo → file:///C:/foo ; Unix: /foo → file:///foo
	if len(abs) >= 2 && abs[1] == ':' {
		abs = "/" + abs
	} else if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return "file://" + abs
}

func imageMIMEFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func readSmallImageBase64(path string, maxBytes int64) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > maxBytes {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(raw), true
}

// isImageToolName reports tools whose text captions typically include path:.
func isImageToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case strings.ToLower(tool.ImageGenerateToolName),
		strings.ToLower(tool.ImageEditToolName),
		strings.ToLower(tool.ViewToolName):
		return true
	default:
		return false
	}
}
