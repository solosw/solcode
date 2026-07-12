package tui

import (
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

// highlightCode attempts to syntax-highlight code content based on file extension.
// Returns the highlighted string and whether highlighting was applied.
func highlightCode(content, filePath string, t Theme, width int) string {
	if content == "" {
		return ""
	}

	lexer := lexerForPath(filePath, content)
	if lexer == nil {
		return ""
	}

	styleName := "monokai"
	if t.Name == "light" {
		styleName = "github"
	}
	style := styles.Get(styleName)
	if style == nil {
		style = styles.Fallback
	}

	// Build a chroma iterator for the content
	iterator, err := lexer.Tokenise(nil, content)
	if err != nil {
		return ""
	}

	// Use terminal formatter with lipgloss-compatible output
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return ""
	}

	var b strings.Builder
	err = formatter.Format(&b, style, iterator)
	if err != nil {
		return ""
	}

	result := b.String()
	if result == "" {
		return ""
	}
	return normalizeANSIBackground(result, t.Background)
}

// highlightCodeBlock detects fenced code blocks with a language hint and highlights them.
// This complements glamour's built-in code highlighting for cases where we want more control.
func highlightCodeBlock(content string, t Theme, width int) string {
	lines := strings.Split(content, "\n")
	_ = width // reserved for future line-width control

	if len(lines) < 3 {
		return ""
	}

	// Look for ```lang ... ``` blocks
	var result strings.Builder
	inBlock := false
	var lang string
	var blockLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") && !inBlock {
			inBlock = true
			lang = strings.TrimPrefix(trimmed, "```")
			lang = strings.TrimSpace(lang)
			blockLines = nil
			continue
		}
		if trimmed == "```" && inBlock {
			inBlock = false
			block := strings.Join(blockLines, "\n")
			lexer := lexerForLang(lang)
			if lexer != nil {
				styleName := "monokai"
				if t.Name == "light" {
					styleName = "github"
				}
				style := styles.Get(styleName)
				if style == nil {
					style = styles.Fallback
				}
				iterator, err := lexer.Tokenise(nil, block)
				if err == nil {
					formatter := formatters.Get("terminal256")
					if formatter != nil {
						var buf strings.Builder
						if err := formatter.Format(&buf, style, iterator); err == nil {
							result.WriteString(buf.String())
							result.WriteString("\n")
							continue
						}
					}
				}
			}
			// Fallback: render as dimmed text
			result.WriteString(t.Dim.Render(block))
			result.WriteString("\n")
			continue
		}
		if inBlock {
			blockLines = append(blockLines, line)
		}
	}

	if result.Len() == 0 {
		return ""
	}
	return normalizeANSIBackground(result.String(), t.Background)
}

// lexerForPath picks a chroma lexer based on file extension.
func lexerForPath(filePath, content string) chroma.Lexer {
	ext := strings.ToLower(filepath.Ext(filePath))
	if lexer := lexers.Match(ext); lexer != nil {
		return lexer
	}
	// Try analysis-based matching
	if lexer := lexers.Analyse(content); lexer != nil {
		return lexer
	}
	return nil
}

// lexerForLang picks a chroma lexer by language name/alias.
func lexerForLang(lang string) chroma.Lexer {
	if lang == "" {
		return nil
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	return lexers.Get(lang)
}

// fileExtension returns the extension including the dot.
func fileExtension(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

// renderCodeWithHighlight renders content as syntax-highlighted code block.
// Used for View/Read tool results that display file contents.
func renderCodeWithHighlight(content, filePath string, t Theme, width int) string {
	highlighted := highlightCode(content, filePath, t, width)
	if highlighted != "" {
		return highlighted
	}
	// Fallback: render with a subtle code style
	codeStyle := lipgloss.NewStyle().
		Foreground(t.Text).
		PaddingLeft(2)
	return codeStyle.Render(content)
}
