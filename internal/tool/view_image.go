package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ViewImageParams is the input schema for the view_image tool.
type ViewImageParams struct {
	FilePath string `json:"file_path"`
}

const (
	ViewImageToolName = "ViewImage"
	MaxImageSize      = 5 * 1024 * 1024 // 5MB
)

// supportedImageExtensions is the set of image file extensions we accept.
var supportedImageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".bmp":  true,
	".tiff": true,
	".tif":  true,
	".svg":  true,
	".ico":  true,
	".heic": true,
	".heif": true,
}

type viewImageTool struct {
	BaseTool
}

// NewViewImageTool creates a new image viewing tool.
func NewViewImageTool() Tool {
	return &viewImageTool{}
}

func (v *viewImageTool) Name() string                             { return ViewImageToolName }
func (v *viewImageTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (v *viewImageTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (v *viewImageTool) Description() string {
	return `Reads an image file from disk and returns it as a base64-encoded data URL so the model can see it.
Use this tool to examine screenshots, diagrams, photos, or other image files.
- Provide file_path (absolute or relative to work dir).
- Maximum file size: 5MB.
- Returns the image encoded as a data URL that the model can process.`
}

func (v *viewImageTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the image file to read (absolute or relative)",
			},
		},
		"required": []string{"file_path"},
	}
}

func (v *viewImageTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params ViewImageParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	if params.FilePath == "" {
		return ErrorResult("file_path is required"), nil
	}

	filePath := resolveImagePath(params.FilePath, uctx)

	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return ErrorResult(fmt.Sprintf("cannot read file: %s: %v", filePath, err)), nil
	}
	if info.IsDir() {
		return ErrorResult(fmt.Sprintf("path is a directory, not an image file: %s", filePath)), nil
	}
	if info.Size() > MaxImageSize {
		return ErrorResult(fmt.Sprintf("file exceeds maximum size of %s: %s", formatImageSize(MaxImageSize), filePath)), nil
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	if !supportedImageExtensions[ext] {
		return ErrorResult(fmt.Sprintf("unsupported image format %q — supported: png, jpg, jpeg, gif, webp, bmp, svg, ico, heic, heif, tiff", ext)), nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("read file %s: %v", filePath, err)), nil
	}

	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = "image/" + strings.TrimPrefix(ext, ".")
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	result := fmt.Sprintf("Image: %s\nSize: %s\nType: %s\n\n![%s](data:%s;base64,%s)",
		filepath.Base(filePath),
		formatImageSize(info.Size()),
		mimeType,
		filepath.Base(filePath),
		mimeType,
		encoded,
	)
	return Result(result), nil
}

func resolveImagePath(filePath string, uctx *UseContext) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	if uctx != nil && uctx.WorkDir != "" {
		return filepath.Join(uctx.WorkDir, filePath)
	}
	return filePath
}

func formatImageSize(sz int64) string {
	const unit = 1024
	if sz < unit {
		return fmt.Sprintf("%d B", sz)
	}
	div, exp := int64(unit), 0
	for n := sz / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(sz)/float64(div), "KMGTPE"[exp])
}
