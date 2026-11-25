package unit_tests

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/solosw/solcode/internal/tool"
)

func TestViewTool_ReadOnly(t *testing.T) {
	vt := tool.NewViewTool()
	if vt.Name() != "View" {
		t.Fatalf("expected View, got %s", vt.Name())
	}
	if !vt.IsReadOnly(nil) {
		t.Fatal("View should be read-only")
	}
}

func TestAddLineNumbers(t *testing.T) {
	result := tool.AddLineNumbers("line1\nline2\nline3", 1)
	expected := "     1|line1\n     2|line2\n     3|line3"
	if result != expected {
		t.Fatalf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestAddLineNumbers_Offset(t *testing.T) {
	result := tool.AddLineNumbers("a\nb", 10)
	expected := "    10|a\n    11|b"
	if result != expected {
		t.Fatalf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestAddLineNumbers_Empty(t *testing.T) {
	if tool.AddLineNumbers("", 1) != "" {
		t.Fatal("expected empty string")
	}
}

func TestIsImagePath(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"photo.jpg", true}, {"icon.png", true}, {"logo.svg", true},
		{"script.go", false}, {"readme.md", false}, {"main.py", false},
	}
	for _, tt := range tests {
		if got := tool.IsImagePath(tt.path); got != tt.expected {
			t.Errorf("IsImagePath(%s) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestSuggestSimilarFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)

	result := tool.SuggestSimilarFile(filepath.Join(dir, "mainx.go"))
	if !result.IsError {
		t.Fatal("expected error for missing file")
	}
	t.Log(result.Text)
}

func TestReadTextFile_PagesWithoutScanningTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	var body strings.Builder
	for i := 1; i <= 20; i++ {
		body.WriteString("line")
		body.WriteByte(byte('0' + i%10))
		body.WriteByte('\n')
	}
	// Append a large tail that must not need full scanning for a small page.
	body.WriteString(strings.Repeat("x\n", 50_000))
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	content, hasMore, err := tool.ReadTextFile(path, 0, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasMore {
		t.Fatal("expected hasMore=true")
	}
	lines := strings.Split(content, "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), content)
	}
}

func TestReadTextFile_OffsetAndEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, hasMore, err := tool.ReadTextFile(path, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("expected hasMore=false at EOF")
	}
	if content != "b\nc" {
		t.Fatalf("content=%q", content)
	}
	content, hasMore, err = tool.ReadTextFile(path, 50, 5)
	if err != nil {
		t.Fatal(err)
	}
	if content != "" || hasMore {
		t.Fatalf("past EOF: content=%q hasMore=%v", content, hasMore)
	}
}

func TestReadTextFile_RejectsBinaryNUL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")
	data := append([]byte("hello"), 0x00, 0x01, 0x02)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := tool.ReadTextFile(path, 0, 10)
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary error, got %v", err)
	}
}

func TestReadTextFile_UTF16LEWithBOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf16.txt")
	text := "hello\n世界\n"
	u16 := utf16.Encode([]rune(text))
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFE})
	for _, c := range u16 {
		_ = binary.Write(&buf, binary.LittleEndian, c)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	content, hasMore, err := tool.ReadTextFile(path, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("expected complete short file")
	}
	if !strings.Contains(content, "hello") || !strings.Contains(content, "世界") {
		t.Fatalf("content=%q", content)
	}
}

func TestViewTool_LargeFileAllowedWithPaging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	// > 250KB but only first lines requested
	line := strings.Repeat("abcdefghijklmnopqrstuvwxyz", 20) + "\n" // ~521 bytes
	var b strings.Builder
	for b.Len() < 300*1024 {
		b.WriteString(line)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	vt := tool.NewViewTool()
	input, _ := json.Marshal(tool.ViewParams{Path: path, Offset: 0, Limit: 5})
	uctx := &tool.UseContext{WorkDir: dir}
	out, err := vt.Invoke(context.Background(), uctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if out.IsError {
		t.Fatalf("unexpected error: %s", out.Text)
	}
	if !strings.Contains(out.Text, "<file>") {
		t.Fatalf("missing file wrapper: %s", out.Text)
	}
	if !strings.Contains(out.Text, "offset=5") {
		t.Fatalf("expected continuation hint, got: %s", out.Text)
	}
}

func TestViewTool_DirectoryMessage(t *testing.T) {
	dir := t.TempDir()
	vt := tool.NewViewTool()
	input, _ := json.Marshal(tool.ViewParams{Path: dir})
	out, err := vt.Invoke(context.Background(), &tool.UseContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatal(err)
	}
	if !out.IsError || !strings.Contains(out.Text, "directory") {
		t.Fatalf("expected directory error, got: %#v", out)
	}
}

func TestViewTool_InvalidImageRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	if err := os.WriteFile(path, []byte("not-really-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	vt := tool.NewViewTool()
	input, _ := json.Marshal(tool.ViewParams{Path: path})
	out, err := vt.Invoke(context.Background(), &tool.UseContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatal(err)
	}
	if !out.IsError || !strings.Contains(out.Text, "load image") {
		t.Fatalf("expected image decoding error, got: %s", out.Text)
	}
}

func TestViewTool_OverlayPagingMatchesDiskFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	fs := &staticTextFS{content: "one\ntwo\nthree\nfour\nfive\n"}
	vt := tool.NewViewTool()
	input, _ := json.Marshal(tool.ViewParams{Path: path, Offset: 1, Limit: 2})
	out, err := vt.Invoke(context.Background(), &tool.UseContext{
		WorkDir:        dir,
		TextFileSystem: fs,
	}, input)
	if err != nil {
		t.Fatal(err)
	}
	if out.IsError {
		t.Fatalf("unexpected error: %s", out.Text)
	}
	if !strings.Contains(out.Text, "     2|two") || !strings.Contains(out.Text, "     3|three") {
		t.Fatalf("bad lines: %s", out.Text)
	}
	if !strings.Contains(out.Text, "offset=3") {
		t.Fatalf("expected hasMore hint: %s", out.Text)
	}
}

type staticTextFS struct {
	content string
}

func (s *staticTextFS) CanReadTextFile() bool  { return true }
func (s *staticTextFS) CanWriteTextFile() bool { return false }
func (s *staticTextFS) ReadTextFile(context.Context, string) (string, error) {
	return s.content, nil
}
func (s *staticTextFS) WriteTextFile(context.Context, string, string) error {
	return nil
}
