package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// renderMarkdown renders markdown text for the terminal using the theme's palette.
// Falls back to the raw text if rendering fails.
func renderMarkdown(text string, theme Theme, width int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if width < 20 {
		width = 80
	}
	style := "dark"
	if theme.Name == "light" {
		style = "light"
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylePath(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return text
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}
