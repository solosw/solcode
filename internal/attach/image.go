package attach

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	imgdraw "image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/tokenest"
	xdraw "golang.org/x/image/draw"

	// Register standard image formats with image.Decode.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// Image optimization knobs. Anthropic normalizes to MaxImageEdgePx anyway;
// we pre-resize so payloads and local estimates stay small.
const (
	// MaxSendImageEdge is the longest edge we keep when attaching images.
	// Matching Anthropic's 1568 limit avoids wasteful oversized uploads.
	MaxSendImageEdge = tokenest.MaxImageEdgePx
	// PreferredMaxImageEdge is a tighter soft limit used for further savings
	// on screenshots / photos while remaining readable for the model.
	PreferredMaxImageEdge = 1280
	// JPEGQuality used when re-encoding optimized images.
	JPEGQuality = 80
)

// ImageAttachment is a converted image ready for the Anthropic API.
type ImageAttachment struct {
	Path       string
	MimeType   string
	Data       string // base64-encoded (after optimization)
	Width      int
	Height     int
	OrigWidth  int
	OrigHeight int
	OrigBytes  int
	Bytes      int
	// Tokens is the estimated Anthropic vision token cost.
	Tokens int
	// Optimized is true when the image was resized or re-encoded.
	Optimized bool
}

// LoadImage reads path, optionally resizes/re-encodes large images, and returns
// a base64-ready attachment with estimated vision tokens.
func LoadImage(path string) (ImageAttachment, error) {
	return loadImage(path)
}

func loadImage(path string) (ImageAttachment, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ImageAttachment{}, err
	}
	if info.Size() > MaxImageBytes {
		return ImageAttachment{}, fmt.Errorf("image exceeds %d bytes", MaxImageBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ImageAttachment{}, err
	}

	ext := strings.ToLower(filepath.Ext(path))
	mimeType := mimeFromExtOrData(ext, data)

	att := ImageAttachment{
		Path:      path,
		MimeType:  mimeType,
		OrigBytes: len(data),
	}

	// Try decode → resize → re-encode. Fall back to raw base64 if decode fails
	// (e.g. webp without decoder, or exotic formats).
	optimized, err := optimizeImage(data, mimeType)
	if err != nil {
		att.Data = base64.StdEncoding.EncodeToString(data)
		att.Bytes = len(data)
		att.Tokens = tokenest.ImageTokensFromBytes(len(data))
		return att, nil
	}
	att.MimeType = optimized.MimeType
	att.Data = optimized.Data
	att.Width = optimized.Width
	att.Height = optimized.Height
	att.OrigWidth = optimized.OrigWidth
	att.OrigHeight = optimized.OrigHeight
	att.Bytes = optimized.Bytes
	att.Tokens = optimized.Tokens
	att.Optimized = optimized.Optimized
	return att, nil
}

type optimizedImage struct {
	MimeType   string
	Data       string
	Width      int
	Height     int
	OrigWidth  int
	OrigHeight int
	Bytes      int
	Tokens     int
	Optimized  bool
}

func optimizeImage(data []byte, mimeType string) (optimizedImage, error) {
	img, format, err := decodeImage(data)
	if err != nil {
		return optimizedImage{}, err
	}
	bounds := img.Bounds()
	origW, origH := bounds.Dx(), bounds.Dy()
	if origW <= 0 || origH <= 0 {
		return optimizedImage{}, fmt.Errorf("invalid image dimensions")
	}

	targetW, targetH := scaleToMaxEdge(origW, origH, PreferredMaxImageEdge)
	if maxInt(targetW, targetH) > MaxSendImageEdge {
		targetW, targetH = scaleToMaxEdge(origW, origH, MaxSendImageEdge)
	}

	resized := img
	changed := false
	if targetW != origW || targetH != origH {
		resized = resizeImage(img, targetW, targetH)
		changed = true
	}

	// Prefer JPEG for photos/screenshots (much smaller).
	outMime := "image/jpeg"
	outBytes, err := encodeJPEG(resized, JPEGQuality)
	if err != nil {
		outBytes, err = encodePNG(resized)
		if err != nil {
			return optimizedImage{}, err
		}
		outMime = "image/png"
	} else {
		// For tiny PNGs, lossless may be smaller than JPEG.
		if (format == "png" || mimeType == "image/png") && !changed {
			pngBytes, pngErr := encodePNG(resized)
			if pngErr == nil && len(pngBytes) < len(outBytes) {
				outBytes = pngBytes
				outMime = "image/png"
			}
		}
		// If we didn't resize and JPEG is larger than original, keep original.
		if !changed && len(outBytes) >= len(data) {
			return optimizedImage{
				MimeType:   mimeType,
				Data:       base64.StdEncoding.EncodeToString(data),
				Width:      origW,
				Height:     origH,
				OrigWidth:  origW,
				OrigHeight: origH,
				Bytes:      len(data),
				Tokens:     tokenest.ImageTokens(origW, origH),
				Optimized:  false,
			}, nil
		}
	}

	if outMime != mimeType {
		changed = true
	}

	return optimizedImage{
		MimeType:   outMime,
		Data:       base64.StdEncoding.EncodeToString(outBytes),
		Width:      targetW,
		Height:     targetH,
		OrigWidth:  origW,
		OrigHeight: origH,
		Bytes:      len(outBytes),
		Tokens:     tokenest.ImageTokens(targetW, targetH),
		Optimized:  changed || len(outBytes) < len(data),
	}, nil
}

func mimeFromExtOrData(ext string, data []byte) string {
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	}
	// Content sniff as fallback.
	if len(data) > 0 {
		if sn := httpDetectImage(data); sn != "" {
			return sn
		}
	}
	if ext != "" {
		return "image/" + strings.TrimPrefix(ext, ".")
	}
	return "image/png"
}

func httpDetectImage(data []byte) string {
	// Local minimal sniff to avoid importing net/http just for this.
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
		return "image/png"
	}
	if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		return "image/gif"
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	return ""
}

func decodeImage(data []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err == nil {
		return img, format, nil
	}
	if img, err2 := png.Decode(bytes.NewReader(data)); err2 == nil {
		return img, "png", nil
	}
	if img, err2 := jpeg.Decode(bytes.NewReader(data)); err2 == nil {
		return img, "jpeg", nil
	}
	if img, err2 := gif.Decode(bytes.NewReader(data)); err2 == nil {
		return img, "gif", nil
	}
	return nil, "", err
}

func scaleToMaxEdge(w, h, maxEdge int) (int, int) {
	if w <= 0 || h <= 0 || maxEdge <= 0 {
		return w, h
	}
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxEdge {
		return w, h
	}
	scale := float64(maxEdge) / float64(longest)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	return nw, nh
}

func resizeImage(src image.Image, width, height int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	rgba, ok := img.(*image.RGBA)
	if !ok {
		b := img.Bounds()
		rgba = image.NewRGBA(b)
		imgdraw.Draw(rgba, b, img, b.Min, imgdraw.Src)
	}
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
