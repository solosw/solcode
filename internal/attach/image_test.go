package attach

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/tokenest"
)

func TestImageTokensFormula(t *testing.T) {
	// 1000x1000 → no resize → 1000*1000/750 = 1333
	if got, want := tokenest.ImageTokens(1000, 1000), 1000*1000/750; got != want {
		t.Fatalf("ImageTokens(1000,1000) = %d, want %d", got, want)
	}
	// Large image normalized to 1568 edge first.
	// 4000x3000 → scale to 1568x1176 → tokens = 1568*1176/750
	w, h := 4000, 3000
	scale := float64(tokenest.MaxImageEdgePx) / 4000.0
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	want := (nw * nh) / tokenest.ImageTokenDivisor
	if got := tokenest.ImageTokens(w, h); got != want {
		t.Fatalf("ImageTokens(4000,3000) = %d, want %d", got, want)
	}
}

func TestOptimizeLargeImageReducesTokens(t *testing.T) {
	dir := t.TempDir()
	// Create a large synthetic PNG (2000x1500 solid color).
	const ow, oh = 2000, 1500
	img := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			img.Set(x, y, color.RGBA{R: 30, G: 120, B: 200, A: 255})
		}
	}
	path := filepath.Join(dir, "big.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	rawTokens := tokenest.ImageTokens(ow, oh)
	att, err := loadImage(path)
	if err != nil {
		t.Fatal(err)
	}
	if att.Tokens <= 0 {
		t.Fatalf("tokens = %d", att.Tokens)
	}
	if att.Width > PreferredMaxImageEdge || att.Height > PreferredMaxImageEdge {
		t.Fatalf("expected resize to max edge %d, got %dx%d", PreferredMaxImageEdge, att.Width, att.Height)
	}
	if att.Tokens >= rawTokens {
		t.Fatalf("optimized tokens %d should be < raw %d (size %dx%d → %dx%d)", att.Tokens, rawTokens, ow, oh, att.Width, att.Height)
	}
	if !att.Optimized {
		t.Fatal("expected Optimized=true for large image")
	}
	// Preferred edge 1280: 2000x1500 → 1280x960 → tokens = 1280*960/750 = 1638
	want := tokenest.ImageTokens(att.Width, att.Height)
	if att.Tokens != want {
		t.Fatalf("tokens = %d, want %d for %dx%d", att.Tokens, want, att.Width, att.Height)
	}
}
