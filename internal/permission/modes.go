package permission

import "strings"

type Mode string

const (
	ModeDefault     Mode = "default"
	ModeAuto        Mode = "auto"
	ModeAcceptEdits Mode = "accept_edits"
	ModeBypass      Mode = "bypass"
	ModePlan        Mode = "plan"
	ModeGoal        Mode = "goal"
	ModeUltra       Mode = "ultra" // Bypass permissions plus Task/workflow execution
	// ModeUltrl is retained as a source-compatible alias for the old spelling.
	ModeUltrl Mode = ModeUltra
)

func NormalizeMode(mode Mode) Mode {
	switch strings.TrimSpace(strings.ToLower(string(mode))) {
	case "", string(ModeDefault), string(ModeAuto):
		return ModeAuto
	case string(ModeAcceptEdits), "acceptedits":
		return ModeAcceptEdits
	case string(ModeBypass), "yolo":
		return ModeBypass
	case string(ModePlan):
		return ModePlan
	case string(ModeGoal):
		return ModeGoal
	case string(ModeUltra), "ultrl":
		return ModeUltra
	default:
		return mode
	}
}

// ModeInfo is shared by the TUI and protocol adapters so supported modes and
// their public names cannot drift between frontends.
type ModeInfo struct {
	ID          Mode
	Name        string
	Description string
}

func AvailableModes() []ModeInfo {
	return []ModeInfo{
		{ModeAuto, "Auto", "Ask before destructive tools"},
		{ModeAcceptEdits, "Accept edits", "Auto-approve file edits; ask for other destructive tools"},
		{ModeBypass, "Bypass", "Skip permission prompts; Task and sub-agents remain disabled"},
		{ModePlan, "Plan", "Read-only tools plus planning tools; no Task or sub-agents"},
		{ModeGoal, "Goal", "Work from goal.md until complete"},
		{ModeUltra, "Ultra", "Bypass permissions and enable Task, sub-agents, and task workflows"},
	}
}

func IsSupportedMode(mode Mode) bool {
	if strings.TrimSpace(string(mode)) == "" {
		return false
	}
	mode = NormalizeMode(mode)
	for _, info := range AvailableModes() {
		if info.ID == mode {
			return true
		}
	}
	return false
}

// Goal is selected through its explicit workflow entry rather than the quick
// permission-mode cycle; all other public permission modes can be cycled.
func CycleModeNames() []string {
	var names []string
	for _, info := range AvailableModes() {
		if info.ID != ModeGoal {
			names = append(names, string(info.ID))
		}
	}
	return names
}
