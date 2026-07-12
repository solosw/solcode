package tui

import "testing"

func TestMarkdownStylesHaveNoCodeBackground(t *testing.T) {
	for _, theme := range []Theme{Dark, Light} {
		style := markdownStyles(theme)
		if style.Code.BackgroundColor != nil {
			t.Fatalf("%s inline code background = %q, want none", theme.Name, *style.Code.BackgroundColor)
		}
		if style.CodeBlock.BackgroundColor != nil {
			t.Fatalf("%s code block background = %q, want none", theme.Name, *style.CodeBlock.BackgroundColor)
		}
		if style.CodeBlock.Chroma != nil && style.CodeBlock.Chroma.Background.BackgroundColor != nil {
			t.Fatalf("%s highlighted code background = %q, want none", theme.Name, *style.CodeBlock.Chroma.Background.BackgroundColor)
		}
	}
}

func TestNormalizeANSIBackgroundPreservesThemeThroughResets(t *testing.T) {
	input := "\x1b[38;2;1;2;3;48;2;4;5;6;1mcode\x1b[0m \x1b[47mtext\x1b[0m"
	background := Dark.WithBackground("#3b3355").Background
	backgroundSequence := "\x1b[48;2;59;51;85m"
	want := backgroundSequence + "\x1b[38;2;1;2;3;1mcode\x1b[0m" + backgroundSequence + " text\x1b[0m" + backgroundSequence
	if got := normalizeANSIBackground(input, background); got != want {
		t.Fatalf("normalizeANSIBackground() = %q, want %q", got, want)
	}
}
