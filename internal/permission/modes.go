package permission

import "strings"

type Mode string

const (
	ModeDefault     Mode = "default"
	ModeAuto        Mode = "auto"
	ModeAcceptEdits Mode = "accept_edits"
	ModeBypass      Mode = "bypass"
	ModePlan        Mode = "plan"
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
	default:
		return mode
	}
}
