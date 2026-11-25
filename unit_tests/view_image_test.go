package unit_tests

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/attach"
	"github.com/solosw/solcode/internal/tokenest"
	"github.com/solosw/solcode/internal/tool"
)

func TestViewTool_ImageOptimizesAndReturnsImageBlock(t *testing.T) {
	dir := t.TempDir()
	const ow, oh = 2000, 1500
	path := filepath.Join(dir, "big.png")
	writeSolidPNG(t, path, ow, oh)

	vt := tool.NewViewTool()
	input, _ := json.Marshal(map[string]string{"path": path})
	block, err := vt.Invoke(context.Background(), &tool.UseContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatal(err)
	}
	if block == nil {
		t.Fatal("nil result")
	}
	if block.IsError {
		t.Fatalf("unexpected error result: %s", block.Text)
	}
	if block.Type != "image" {
		t.Fatalf("type = %q, want image", block.Type)
	}
	if block.Data == "" {
		t.Fatal("expected base64 image data")
	}
	if !strings.HasPrefix(block.MimeType, "image/") {
		t.Fatalf("mime = %q", block.MimeType)
	}
	// Caption should NOT embed raw base64 data URL.
	if strings.Contains(block.Text, "data:image") || strings.Contains(block.Text, "base64,") {
		t.Fatalf("caption should not contain data URL, got: %s", block.Text)
	}
	if !strings.Contains(block.Text, "vision tokens") {
		t.Fatalf("caption missing token estimate: %s", block.Text)
	}
	if !strings.Contains(block.Text, "2000x1500") {
		t.Fatalf("caption missing original size: %s", block.Text)
	}

	// Optimized dimensions should match attach pipeline preferred edge.
	att, err := attach.LoadImage(path)
	if err != nil {
		t.Fatal(err)
	}
	if att.Width > attach.PreferredMaxImageEdge || att.Height > attach.PreferredMaxImageEdge {
		t.Fatalf("expected resize ≤%d, got %dx%d", attach.PreferredMaxImageEdge, att.Width, att.Height)
	}
	rawTokens := tokenest.ImageTokens(ow, oh)
	if att.Tokens >= rawTokens {
		t.Fatalf("optimized tokens %d should be < raw %d", att.Tokens, rawTokens)
	}
}

func TestViewTool_ImageMissingFile(t *testing.T) {
	vt := tool.NewViewTool()
	input, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "nope.png")})
	block, err := vt.Invoke(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if block == nil || !block.IsError {
		t.Fatal("expected error result for missing file")
	}
}

func TestViewTool_ImageIgnoresTextOverlayAndPaging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	writeSolidPNG(t, path, 16, 12)
	input, _ := json.Marshal(tool.ViewParams{Path: path, Offset: 100, Limit: 1})
	block, err := tool.NewViewTool().Invoke(t.Context(), &tool.UseContext{WorkDir: dir, TextFileSystem: &staticTextFS{content: "editor text"}}, input)
	if err != nil || block == nil || block.IsError || block.Type != "image" || block.Data == "" {
		t.Fatalf("image must use local image reader: %+v (%v)", block, err)
	}
}

func TestViewTool_RejectsOversizedImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(tool.MaxImageSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	input, _ := json.Marshal(tool.ViewParams{Path: path})
	block, err := tool.NewViewTool().Invoke(t.Context(), nil, input)
	if err != nil || block == nil || !block.IsError || !strings.Contains(block.Text, "maximum size") {
		t.Fatalf("expected image size rejection: %+v (%v)", block, err)
	}
}

func writeSolidPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 40, G: 90, B: 180, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
