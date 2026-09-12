package tool

import (
	"encoding/json"
	"strings"
)

// CheckpointableFileTools are mutation tools whose paths are snapshotted
// before Invoke for code-only rewind.
func CheckpointableFileTool(name string) bool {
	switch name {
	case EditToolName, WriteToolName, PatchToolName, MultiEditToolName, MultiWriteToolName:
		return true
	default:
		return false
	}
}

// PathsForCheckpoint extracts workspace paths from a file-mutation tool input.
func PathsForCheckpoint(toolName string, input json.RawMessage) []string {
	switch toolName {
	case EditToolName, WriteToolName, PatchToolName:
		var params struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(input, &params) != nil {
			return nil
		}
		path := strings.TrimSpace(params.Path)
		if path == "" {
			return nil
		}
		return []string{path}
	case MultiEditToolName:
		var params MultiEditParams
		if json.Unmarshal(input, &params) != nil {
			return nil
		}
		return uniquePaths(func(yield func(string)) {
			for _, edit := range params.Edits {
				yield(edit.Path)
			}
		})
	case MultiWriteToolName:
		var params MultiWriteParams
		if json.Unmarshal(input, &params) != nil {
			return nil
		}
		return uniquePaths(func(yield func(string)) {
			for _, file := range params.Files {
				yield(file.Path)
			}
		})
	default:
		return nil
	}
}

func uniquePaths(iter func(func(string))) []string {
	seen := map[string]bool{}
	var out []string
	iter(func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	})
	return out
}
