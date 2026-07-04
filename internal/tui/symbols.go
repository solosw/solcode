package tui

// Claude Code-style message markers and connectors.
const (
	UserMark      = "❯"
	AssistantMark = "●"
	SystemMark    = "▶"
	ErrorMark     = "⚠"
	Connector     = "  ⎿  "
	Continuation  = "  │  "
	PromptPrefix  = "> "
)

// Spinner frames (Claude style, forward then reverse).
var SpinnerFrames = []string{"·", "✢", "*", "✶", "✻", "✽", "✻", "✶", "*", "✢"}
