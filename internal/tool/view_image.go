package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/attach"
)

const MaxImageSize = attach.MaxImageBytes // 5MB

// Anthropic vision accepts only these media types in image blocks.
var anthropicImageMimes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

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

func viewImage(filePath string) (*ContentBlock, error) {

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

	att, err := attach.LoadImage(filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("load image %s: %v", filePath, err)), nil
	}
	if att.Width == 0 && (att.MimeType == "image/png" || att.MimeType == "image/jpeg" || att.MimeType == "image/gif") {
		return ErrorResult(fmt.Sprintf("load image %s: invalid image data", filePath)), nil
	}
	if !anthropicImageMimes[att.MimeType] {
		return ErrorResult(fmt.Sprintf(
			"image format %q cannot be sent as a vision block (need jpeg/png/gif/webp after decode). Convert the file or use a different format: %s",
			att.MimeType, filePath,
		)), nil
	}
	if att.Data == "" {
		return ErrorResult(fmt.Sprintf("image produced empty payload: %s", filePath)), nil
	}

	return ImageResult(att.MimeType, att.Data, formatViewImageCaption(filePath, att)), nil
}

func formatViewImageCaption(filePath string, img attach.ImageAttachment) string {
	parts := []string{
		fmt.Sprintf("Image: %s", filepath.Base(filePath)),
	}
	if img.Width > 0 && img.Height > 0 {
		if img.Optimized && (img.OrigWidth != img.Width || img.OrigHeight != img.Height) {
			parts = append(parts, fmt.Sprintf("Size: %dx%d→%dx%d", img.OrigWidth, img.OrigHeight, img.Width, img.Height))
		} else {
			parts = append(parts, fmt.Sprintf("Size: %dx%d", img.Width, img.Height))
		}
	} else {
		parts = append(parts, fmt.Sprintf("Bytes: %s", formatImageSize(int64(img.Bytes))))
	}
	if img.MimeType != "" {
		parts = append(parts, "Type: "+img.MimeType)
	}
	if img.Tokens > 0 {
		parts = append(parts, fmt.Sprintf("~%d vision tokens", img.Tokens))
	}
	if img.Optimized && img.OrigBytes > 0 && img.Bytes > 0 && img.Bytes < img.OrigBytes {
		parts = append(parts, fmt.Sprintf("compressed %s→%s", formatImageSize(int64(img.OrigBytes)), formatImageSize(int64(img.Bytes))))
	}
	parts = append(parts, "Path: "+filePath)
	return strings.Join(parts, "\n")
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
