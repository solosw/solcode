// Package attach expands @path references in user prompts into text and
// multimodal content blocks for the model API.
package attach

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/tokenest"
)

const (
	// MaxImageBytes is the maximum size of an attached image.
	MaxImageBytes = 5 * 1024 * 1024
	// MaxTextBytes is the maximum size of an attached text file to inline.
	MaxTextBytes = 250 * 1024
	// MaxTextLines limits how many lines of a text file are inlined.
	MaxTextLines = 2000
	// MaxFileSuggestions caps @path autocomplete results.
	MaxFileSuggestions = 20
)

// atRefPattern matches @path tokens that are not email-like addresses.
// Supports quoted paths: @"path with spaces/file.go"
var atRefPattern = regexp.MustCompile(`@(?:"([^"]+)"|([^\s@]+))`)

// Image extensions accepted for multimodal conversion.
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".tiff": true, ".tif": true, ".ico": true,
}

// Ref is a single @path reference found in a prompt.
type Ref struct {
	Raw      string // full match including @
	Path     string // path without @
	Start    int
	End      int
	AbsPath  string
	IsImage  bool
	IsDir    bool
	Exists   bool
	MimeType string
}

// Expanded is the result of expanding @refs in a prompt.
type Expanded struct {
	// DisplayText is the human-readable prompt with paths preserved.
	DisplayText string
	// Text is the model-facing text (paths + inlined file contents for non-images).
	Text string
	// Images holds base64-ready image attachments.
	Images []ImageAttachment
	// ImageTokens is the sum of estimated vision tokens for attached images.
	ImageTokens int
	// Refs lists all discovered references.
	Refs []Ref
}

// EstimatedTokens approximates total tokens for the expanded prompt
// (text + image vision tokens).
func (e Expanded) EstimatedTokens() int {
	return tokenest.Text(e.Text) + e.ImageTokens
}

// ParseRefs finds @path tokens in prompt.
func ParseRefs(prompt string) []Ref {
	matches := atRefPattern.FindAllStringSubmatchIndex(prompt, -1)
	if len(matches) == 0 {
		return nil
	}
	refs := make([]Ref, 0, len(matches))
	for _, m := range matches {
		if len(m) < 6 {
			continue
		}
		start, end := m[0], m[1]
		// Skip email-like: char before @ is alphanumeric
		if start > 0 {
			prev := rune(prompt[start-1])
			if unicode.IsLetter(prev) || unicode.IsDigit(prev) {
				continue
			}
		}
		path := ""
		if m[2] >= 0 && m[3] >= 0 {
			path = prompt[m[2]:m[3]]
		} else if m[4] >= 0 && m[5] >= 0 {
			path = prompt[m[4]:m[5]]
		}
		path = strings.TrimSpace(path)
		if path == "" || path == "/" || path == "." {
			continue
		}
		refs = append(refs, Ref{
			Raw:   prompt[start:end],
			Path:  path,
			Start: start,
			End:   end,
		})
	}
	return refs
}

// Expand resolves @refs relative to workDir, inlines text files, and converts images.
func Expand(prompt, workDir string) Expanded {
	out := Expanded{
		DisplayText: prompt,
		Text:        prompt,
	}
	refs := ParseRefs(prompt)
	if len(refs) == 0 {
		return out
	}

	var textParts []string
	// Keep the original prompt text first so the model still sees the user's words.
	textParts = append(textParts, strings.TrimSpace(prompt))

	seen := map[string]bool{}
	for i := range refs {
		ref := &refs[i]
		abs := resolvePath(ref.Path, workDir)
		ref.AbsPath = abs
		info, err := os.Stat(abs)
		if err != nil {
			ref.Exists = false
			textParts = append(textParts, fmt.Sprintf("\n\n[attached file not found: %s]", ref.Path))
			continue
		}
		ref.Exists = true
		if info.IsDir() {
			ref.IsDir = true
			textParts = append(textParts, fmt.Sprintf("\n\n[attached directory: %s — use LS/Glob tools to explore]", ref.Path))
			continue
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true

		if isImagePath(abs) {
			ref.IsImage = true
			img, err := loadImage(abs)
			if err != nil {
				textParts = append(textParts, fmt.Sprintf("\n\n[failed to attach image %s: %v]", ref.Path, err))
				continue
			}
			ref.MimeType = img.MimeType
			out.Images = append(out.Images, img)
			out.ImageTokens += img.Tokens
			note := formatImageAttachNote(ref.Path, img)
			textParts = append(textParts, "\n\n"+note)
			continue
		}

		content, err := loadTextFile(abs)
		if err != nil {
			textParts = append(textParts, fmt.Sprintf("\n\n[failed to attach file %s: %v]", ref.Path, err))
			continue
		}
		textParts = append(textParts, fmt.Sprintf("\n\n<attached_file path=%q>\n%s\n</attached_file>", ref.Path, content))
	}

	out.Refs = refs
	out.Text = strings.TrimSpace(strings.Join(textParts, ""))
	return out
}

// UserMessage builds a multimodal user message from an expanded prompt.
func UserMessage(expanded Expanded) sdk.MessageParam {
	blocks := make([]sdk.ContentBlockParamUnion, 0, 1+len(expanded.Images))
	if text := strings.TrimSpace(expanded.Text); text != "" {
		blocks = append(blocks, sdk.NewTextBlock(text))
	}
	for _, img := range expanded.Images {
		blocks = append(blocks, ImageBlock(img.MimeType, img.Data))
	}
	if len(blocks) == 0 {
		return sdk.NewUserMessage(sdk.NewTextBlock(expanded.DisplayText))
	}
	return sdk.NewUserMessage(blocks...)
}

// ImageBlock constructs an Anthropic image content block from base64 data.
func ImageBlock(mimeType, data string) sdk.ContentBlockParamUnion {
	mt := sdk.Base64ImageSourceMediaType(mimeType)
	return sdk.ContentBlockParamUnion{
		OfImage: &sdk.ImageBlockParam{
			Source: sdk.ImageBlockParamSourceUnion{
				OfBase64: &sdk.Base64ImageSourceParam{
					MediaType: mt,
					Data:      data,
				},
			},
		},
	}
}

// SuggestFiles returns file/dir suggestions for @autocomplete under workDir.
// query is the path prefix after @ (may include subdirectories).
func SuggestFiles(workDir, query string) []string {
	query = strings.TrimSpace(query)
	query = strings.ReplaceAll(query, "\\", "/")

	// Split into directory prefix and name filter.
	dirPart := ""
	namePart := query
	if i := strings.LastIndex(query, "/"); i >= 0 {
		dirPart = query[:i]
		namePart = query[i+1:]
	}

	searchDir := workDir
	if dirPart != "" {
		searchDir = resolvePath(dirPart, workDir)
	}

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil
	}

	nameLower := strings.ToLower(namePart)
	var matches []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if nameLower != "" && !strings.HasPrefix(strings.ToLower(name), nameLower) {
			continue
		}
		rel := name
		if dirPart != "" {
			rel = dirPart + "/" + name
		}
		if entry.IsDir() {
			rel += "/"
		}
		matches = append(matches, rel)
		if len(matches) >= MaxFileSuggestions {
			break
		}
	}
	sort.Strings(matches)
	return matches
}

// CurrentAtToken returns the @token being typed at the end of input, if any.
// Returns (prefix_after_at, start_index, ok).
func CurrentAtToken(input string) (prefix string, start int, ok bool) {
	// Find the last unquoted @ that starts a token near the end.
	// Walk from the end: the current token is everything after the last whitespace
	// (or start of string). If that token starts with @, it's an at-token.
	trimmedRight := input
	// Keep trailing content as-is for cursor-at-end UX.
	lastWS := -1
	for i := len(trimmedRight) - 1; i >= 0; i-- {
		if unicode.IsSpace(rune(trimmedRight[i])) {
			lastWS = i
			break
		}
	}
	tokenStart := lastWS + 1
	if tokenStart >= len(input) {
		return "", 0, false
	}
	token := input[tokenStart:]
	if !strings.HasPrefix(token, "@") {
		return "", 0, false
	}
	// Don't treat emails as file refs: if there's a letter/digit before @, skip.
	if tokenStart > 0 {
		prev := rune(input[tokenStart-1])
		if unicode.IsLetter(prev) || unicode.IsDigit(prev) {
			return "", 0, false
		}
	}
	prefix = token[1:]
	// Quoted path still open: @"foo
	if strings.HasPrefix(prefix, `"`) {
		prefix = strings.TrimPrefix(prefix, `"`)
		if strings.Contains(prefix, `"`) {
			// closed quote — not actively completing
			return "", 0, false
		}
	}
	return prefix, tokenStart, true
}

func resolvePath(path, workDir string) string {
	path = filepath.FromSlash(path)
	if filepath.IsAbs(path) {
		return path
	}
	if workDir == "" {
		return path
	}
	return filepath.Join(workDir, path)
}

func isImagePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return imageExts[ext]
}

func formatImageAttachNote(path string, img ImageAttachment) string {
	// Keep the model-facing note short; token cost is still counted via ImageTokens.
	parts := []string{fmt.Sprintf("attached image: %s", path)}
	if img.Width > 0 && img.Height > 0 {
		if img.Optimized && (img.OrigWidth != img.Width || img.OrigHeight != img.Height) {
			parts = append(parts, fmt.Sprintf("%dx%d→%dx%d", img.OrigWidth, img.OrigHeight, img.Width, img.Height))
		} else {
			parts = append(parts, fmt.Sprintf("%dx%d", img.Width, img.Height))
		}
	}
	if img.MimeType != "" {
		parts = append(parts, img.MimeType)
	}
	if img.Tokens > 0 {
		parts = append(parts, fmt.Sprintf("~%d tokens", img.Tokens))
	}
	if img.Optimized && img.OrigBytes > 0 && img.Bytes > 0 && img.Bytes < img.OrigBytes {
		parts = append(parts, fmt.Sprintf("compressed %s→%s", formatBytes(img.OrigBytes), formatBytes(img.Bytes)))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func loadTextFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > MaxTextBytes {
		return "", fmt.Errorf("file exceeds %d bytes", MaxTextBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Reject likely binary
	if isBinary(data) {
		return "", fmt.Errorf("binary file not inlined; path recorded only")
	}
	text := string(data)
	lines := strings.Split(text, "\n")
	if len(lines) > MaxTextLines {
		lines = lines[:MaxTextLines]
		text = strings.Join(lines, "\n") + fmt.Sprintf("\n... (%d more lines truncated)", len(strings.Split(string(data), "\n"))-MaxTextLines)
	}
	return text, nil
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}
