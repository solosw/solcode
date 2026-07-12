package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
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
	markdownStyle := markdownStyles(theme)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return text
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(normalizeANSIBackground(out, theme.Background), "\n")
}

// normalizeANSIBackground removes ANSI background-color parameters while
// preserving the configured TUI background across reset sequences. Syntax
// renderers emit ESC[0m between tokens, which otherwise restores the terminal's
// default (often black) background inside a custom-colored TUI.
func normalizeANSIBackground(text string, background lipgloss.Color) string {
	backgroundSequence := ansiBackgroundSequence(background)
	var output strings.Builder
	output.WriteString(backgroundSequence)
	for index := 0; index < len(text); {
		if text[index] != '\x1b' || index+1 >= len(text) || text[index+1] != '[' {
			output.WriteByte(text[index])
			index++
			continue
		}

		end := index + 2
		for end < len(text) && (text[end] == ';' || (text[end] >= '0' && text[end] <= '9')) {
			end++
		}
		if end == len(text) || text[end] != 'm' {
			output.WriteByte(text[index])
			index++
			continue
		}

		params := strings.Split(text[index+2:end], ";")
		kept := make([]string, 0, len(params))
		resetBackground := len(params) == 1 && params[0] == ""
		for paramIndex := 0; paramIndex < len(params); paramIndex++ {
			param := params[paramIndex]
			switch param {
			case "", "0":
				resetBackground = true
				kept = append(kept, "0")
				continue
			case "40", "41", "42", "43", "44", "45", "46", "47", "49", "100", "101", "102", "103", "104", "105", "106", "107":
				continue
			case "48":
				if paramIndex+1 < len(params) {
					paramIndex++
					if params[paramIndex] == "2" && paramIndex+3 < len(params) {
						paramIndex += 3
					} else if params[paramIndex] == "5" && paramIndex+1 < len(params) {
						paramIndex++
					}
				}
				continue
			}
			kept = append(kept, param)
		}
		if len(kept) > 0 {
			output.WriteString("\x1b[")
			output.WriteString(strings.Join(kept, ";"))
			output.WriteByte('m')
		}
		if resetBackground {
			output.WriteString(backgroundSequence)
		}
		index = end + 1
	}
	return output.String()
}

func ansiBackgroundSequence(color lipgloss.Color) string {
	value := string(color)
	if len(value) == 7 && value[0] == '#' {
		red, redErr := strconv.ParseUint(value[1:3], 16, 8)
		green, greenErr := strconv.ParseUint(value[3:5], 16, 8)
		blue, blueErr := strconv.ParseUint(value[5:7], 16, 8)
		if redErr == nil && greenErr == nil && blueErr == nil {
			return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", red, green, blue)
		}
	}
	if index, err := strconv.Atoi(value); err == nil && index >= 0 && index <= 255 {
		return fmt.Sprintf("\x1b[48;5;%dm", index)
	}
	return ""
}

// markdownStyles starts with Glamour's built-in palette but removes code
// backgrounds so assistant output inherits the configured TUI background.
func markdownStyles(theme Theme) ansi.StyleConfig {
	style := styles.DarkStyleConfig
	if theme.Name == "light" {
		style = styles.LightStyleConfig
	}

	style.Code.BackgroundColor = nil
	style.CodeBlock.BackgroundColor = nil
	if style.CodeBlock.Chroma != nil {
		chroma := *style.CodeBlock.Chroma
		chroma.Background.BackgroundColor = nil
		style.CodeBlock.Chroma = &chroma
	}
	return style
}
