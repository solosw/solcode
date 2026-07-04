package tui

import "github.com/charmbracelet/lipgloss"

// Theme holds the color palette and derived lipgloss styles for the TUI.
// Two built-in palettes are provided: Dark and Light, mimicking Claude Code.
type Theme struct {
	Name string

	Claude       lipgloss.Color
	ClaudeShimmer lipgloss.Color
	Text         lipgloss.Color
	Inactive     lipgloss.Color
	Subtle       lipgloss.Color
	Suggestion   lipgloss.Color
	Success      lipgloss.Color
	Error        lipgloss.Color
	Warning      lipgloss.Color
	Permission   lipgloss.Color
	PromptBorder lipgloss.Color
	Background   lipgloss.Color
	StatusBG     lipgloss.Color
	StatusFG     lipgloss.Color

	// Derived styles
	User        lipgloss.Style
	Assistant   lipgloss.Style
	System      lipgloss.Style
	ErrorStyle  lipgloss.Style
	Tool        lipgloss.Style
	ToolDone    lipgloss.Style
	ToolResult  lipgloss.Style
	Connector   lipgloss.Style
	Dim         lipgloss.Style
	ClaudeStyle lipgloss.Style
	Status      lipgloss.Style
	Prompt      lipgloss.Style
	PermBorder  lipgloss.Style
	PermTitle   lipgloss.Style
	PermHint    lipgloss.Style
	DiffAdd     lipgloss.Style
	DiffDel     lipgloss.Style
	DiffCtx     lipgloss.Style
	DiffHdr     lipgloss.Style
}

var Dark = buildTheme("dark", themePalette{
	claude:        "#d77757",
	claudeShimmer: "#e8a98a",
	text:          "#ffffff",
	inactive:      "#999999",
	subtle:        "#505050",
	suggestion:    "#6495ed",
	success:       "#4eba65",
	err:           "#ff6b80",
	warning:       "#ffc107",
	permission:    "#b1b9f9",
	promptBorder:  "#888888",
	background:    "#000000",
	statusBG:      "236",
	statusFG:      "250",
})

var Light = buildTheme("light", themePalette{
	claude:        "#b85c3a",
	claudeShimmer: "#d98a6a",
	text:          "#222222",
	inactive:      "#666666",
	subtle:        "#aaaaaa",
	suggestion:    "#3b6fd4",
	success:       "#2f8a46",
	err:           "#d23a52",
	warning:       "#c98a00",
	permission:    "#6f78c8",
	promptBorder:  "#bbbbbb",
	background:    "#fafafa",
	statusBG:      "253",
	statusFG:      "238",
})

type themePalette struct {
	claude, claudeShimmer, text, inactive, subtle, suggestion string
	success, err, warning, permission, promptBorder          string
	background, statusBG, statusFG                            string
}

func buildTheme(name string, p themePalette) Theme {
	t := Theme{
		Name:          name,
		Claude:        lipgloss.Color(p.claude),
		ClaudeShimmer: lipgloss.Color(p.claudeShimmer),
		Text:          lipgloss.Color(p.text),
		Inactive:      lipgloss.Color(p.inactive),
		Subtle:        lipgloss.Color(p.subtle),
		Suggestion:    lipgloss.Color(p.suggestion),
		Success:       lipgloss.Color(p.success),
		Error:         lipgloss.Color(p.err),
		Warning:       lipgloss.Color(p.warning),
		Permission:    lipgloss.Color(p.permission),
		PromptBorder:  lipgloss.Color(p.promptBorder),
		Background:    lipgloss.Color(p.background),
		StatusBG:      lipgloss.Color(p.statusBG),
		StatusFG:      lipgloss.Color(p.statusFG),
	}
	t.User = lipgloss.NewStyle().Foreground(t.Claude).Bold(true)
	t.Assistant = lipgloss.NewStyle().Foreground(t.Claude)
	t.System = lipgloss.NewStyle().Foreground(t.Suggestion).Italic(true)
	t.ErrorStyle = lipgloss.NewStyle().Foreground(t.Error).Bold(true)
	t.Tool = lipgloss.NewStyle().Foreground(t.Claude)
	t.ToolDone = lipgloss.NewStyle().Foreground(t.Success)
	t.ToolResult = lipgloss.NewStyle().Faint(true)
	t.Connector = lipgloss.NewStyle().Faint(true)
	t.Dim = lipgloss.NewStyle().Foreground(t.Inactive)
	t.ClaudeStyle = lipgloss.NewStyle().Foreground(t.Claude).Bold(true)
	t.Status = lipgloss.NewStyle().Background(t.StatusBG).Foreground(t.StatusFG).Padding(0, 1)
	t.Prompt = lipgloss.NewStyle().Foreground(t.Claude).Bold(true)
	t.PermBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Permission).
		BorderBottom(false).BorderLeft(false).BorderRight(false).
		MarginTop(1)
	t.PermTitle = lipgloss.NewStyle().Foreground(t.Permission).Bold(true)
	t.PermHint = lipgloss.NewStyle().Foreground(t.Inactive)
	t.DiffAdd = lipgloss.NewStyle().Foreground(t.Success)
	t.DiffDel = lipgloss.NewStyle().Foreground(t.Error)
	t.DiffCtx = lipgloss.NewStyle().Foreground(t.Inactive)
	t.DiffHdr = lipgloss.NewStyle().Foreground(t.Suggestion).Bold(true)
	return t
}

func ThemeByName(name string) Theme {
	if name == "light" {
		return Light
	}
	return Dark
}
