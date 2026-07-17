package attach

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRefs(t *testing.T) {
	refs := ParseRefs(`look at @internal/engine/engine.go and @"path with space/a.go"`)
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2: %#v", len(refs), refs)
	}
	if refs[0].Path != "internal/engine/engine.go" {
		t.Fatalf("path0 = %q", refs[0].Path)
	}
	if refs[1].Path != "path with space/a.go" {
		t.Fatalf("path1 = %q", refs[1].Path)
	}
}

func TestParseRefsSkipsEmail(t *testing.T) {
	refs := ParseRefs("email me at user@example.com please")
	if len(refs) != 0 {
		t.Fatalf("expected no refs for email, got %#v", refs)
	}
}

func TestCurrentAtToken(t *testing.T) {
	prefix, start, ok := CurrentAtToken("please check @inte")
	if !ok || prefix != "inte" || start != 13 {
		t.Fatalf("prefix=%q start=%d ok=%v", prefix, start, ok)
	}
	_, _, ok = CurrentAtToken("no at token here")
	if ok {
		t.Fatal("expected no at token")
	}
	prefix, _, ok = CurrentAtToken("@")
	if !ok || prefix != "" {
		t.Fatalf("bare @ failed: prefix=%q ok=%v", prefix, ok)
	}
}

func TestExpandTextAndImage(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "hello.go")
	if err := os.WriteFile(textPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// minimal 1x1 PNG
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
		0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xfe, 0xd4, 0xef, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	imgPath := filepath.Join(dir, "dot.png")
	if err := os.WriteFile(imgPath, png, 0o644); err != nil {
		t.Fatal(err)
	}

	expanded := Expand("see @hello.go and @dot.png", dir)
	if len(expanded.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(expanded.Images))
	}
	// Optimization may re-encode tiny PNG as JPEG; accept either.
	switch expanded.Images[0].MimeType {
	case "image/png", "image/jpeg":
	default:
		t.Fatalf("mime = %q", expanded.Images[0].MimeType)
	}
	if expanded.Images[0].Tokens <= 0 {
		t.Fatalf("expected positive image token estimate, got %d", expanded.Images[0].Tokens)
	}
	if expanded.ImageTokens != expanded.Images[0].Tokens {
		t.Fatalf("ImageTokens = %d, image.Tokens = %d", expanded.ImageTokens, expanded.Images[0].Tokens)
	}
	if expanded.EstimatedTokens() <= expanded.ImageTokens {
		t.Fatalf("EstimatedTokens() = %d should include text + images", expanded.EstimatedTokens())
	}
	if !strings.Contains(expanded.Text, "package main") {
		t.Fatalf("expected inlined text, got %q", expanded.Text)
	}
	if !strings.Contains(expanded.Text, "attached image") {
		t.Fatalf("expected image note, got %q", expanded.Text)
	}
	if !strings.Contains(expanded.Text, "tokens") {
		t.Fatalf("expected token note in attach text, got %q", expanded.Text)
	}

	msg := UserMessage(expanded)
	if len(msg.Content) < 2 {
		t.Fatalf("expected text+image blocks, got %d", len(msg.Content))
	}
	if msg.Content[0].OfText == nil {
		t.Fatal("first block should be text")
	}
	// find image block
	foundImg := false
	for _, b := range msg.Content {
		if b.OfImage != nil && b.OfImage.Source.OfBase64 != nil {
			foundImg = true
			if b.OfImage.Source.OfBase64.Data == "" {
				t.Fatal("empty image data")
			}
		}
	}
	if !foundImg {
		t.Fatal("expected image block in user message")
	}
}

func TestSuggestFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alpha.go"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "beta.go"), []byte("b"), 0o644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(dir, "subdir", "gamma.go"), []byte("g"), 0o644)

	matches := SuggestFiles(dir, "a")
	if len(matches) == 0 || matches[0] != "alpha.go" {
		t.Fatalf("matches = %#v", matches)
	}
	matches = SuggestFiles(dir, "subdir/")
	found := false
	for _, m := range matches {
		if strings.Contains(m, "gamma") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected gamma under subdir, got %#v", matches)
	}
}
