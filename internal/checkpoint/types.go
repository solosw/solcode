package checkpoint

import "time"

const SchemaVersion = 1

// DefaultRetainTurns keeps the newest N checkpoints.
const DefaultRetainTurns = 50

// FileSnap is the turn-start content of one workspace file.
// Content == nil means the file did not exist at the anchor (restore deletes it).
type FileSnap struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

// Checkpoint anchors one user turn and the files first touched during that turn.
type Checkpoint struct {
	Turn   int        `json:"turn"`
	Time   time.Time  `json:"time"`
	Prompt string     `json:"prompt"`
	Name   string     `json:"name,omitempty"`
	Files  []FileSnap `json:"files,omitempty"`
}

// Meta is the lightweight list entry shown in /rewind.
type Meta struct {
	Turn      int       `json:"turn"`
	Time      time.Time `json:"time"`
	Prompt    string    `json:"prompt"`
	Name      string    `json:"name,omitempty"`
	FileCount int       `json:"file_count"`
}

// RestoreResult summarizes a code-only rewind.
type RestoreResult struct {
	Restored []string `json:"restored,omitempty"`
	Deleted  []string `json:"deleted,omitempty"`
	Skipped  []string `json:"skipped,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

func (r RestoreResult) Partial() bool {
	return len(r.Errors) > 0 || len(r.Skipped) > 0
}

func (r RestoreResult) Empty() bool {
	return len(r.Restored) == 0 && len(r.Deleted) == 0 && len(r.Skipped) == 0 && len(r.Errors) == 0
}
