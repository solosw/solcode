package engine

import (
	"encoding/json"
	"sort"
)

type workingSetState struct {
	Goal  string
	Files []workingFileState
	Facts []string
	Open  []string
}

type workingFileState struct {
	Path       string
	Hash       string
	Modified   bool
	PrefixHash bool
}

// Snapshot stores bounded metadata, not another copy of tool output. Restored
// hashes are advisory and files are marked stale until explicitly re-read.
func (w *WorkingSet) Snapshot() json.RawMessage {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	state := workingSetState{Goal: oneLine(w.goal, 200), Facts: w.facts, Open: w.open}
	for _, f := range w.files {
		state.Files = append(state.Files, workingFileState{Path: f.path, Hash: f.hash, Modified: f.modified, PrefixHash: f.prefixHash})
	}
	// Stable ordering makes fork snapshots content-addressable across calls.
	sort.Slice(state.Files, func(i, j int) bool { return state.Files[i].Path < state.Files[j].Path })
	data, _ := json.Marshal(state)
	return data
}

func RestoreWorkingSet(data json.RawMessage) *WorkingSet {
	w := NewWorkingSet()
	var state workingSetState
	if len(data) == 0 || json.Unmarshal(data, &state) != nil {
		return w
	}
	w.goal = state.Goal
	for _, f := range state.Files {
		if f.Path != "" && len(w.files) < w.maxFiles {
			w.files[f.Path] = &workingFile{path: f.Path, hash: f.Hash, modified: f.Modified, prefixHash: f.PrefixHash, stale: true}
		}
	}
	for _, fact := range state.Facts {
		w.AddFact(fact)
	}
	for _, question := range state.Open {
		w.AddOpen(question)
	}
	return w
}
