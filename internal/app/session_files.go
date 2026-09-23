package app

import (
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/checkpoint"
)

func init() {
	// Checkpoint capture shares the same noise rules as session-memory file lists.
	checkpoint.IsUnimportantPath = isUnimportantSessionFile
}

// isUnimportantSessionFile reports paths that should never be retained in
// session-memory file lists or checkpoint captures. These are bookkeeping,
// caches, locks, and other noise unrelated to the user's task.
func isUnimportantSessionFile(path string) bool {
	path = strings.TrimSpace(filepath.ToSlash(path))
	if path == "" {
		return true
	}
	lower := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(lower))

	if strings.HasPrefix(lower, ".solcode/") || strings.Contains(lower, "/.solcode/") {
		return true
	}
	noiseDirs := []string{
		"/.git/", "/node_modules/", "/vendor/", "/.idea/", "/.vscode/",
		"/__pycache__/", "/.cache/", "/dist/", "/build/", "/coverage/",
		"/tmp/", "/.tmp/",
	}
	for _, dir := range noiseDirs {
		if strings.Contains(lower, dir) || strings.HasPrefix(lower, strings.TrimPrefix(dir, "/")) {
			return true
		}
	}
	noiseNames := map[string]bool{
		"todos.json": true, "go.sum": true, "package-lock.json": true,
		"yarn.lock": true, "pnpm-lock.yaml": true, "cargo.lock": true,
		".ds_store": true, "thumbs.db": true,
	}
	if noiseNames[base] {
		return true
	}
	noiseExt := map[string]bool{
		".lock": true, ".tmp": true, ".temp": true, ".log": true,
		".pyc": true, ".pyo": true, ".o": true, ".obj": true,
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".map": true, ".min.js": true, ".min.css": true,
	}
	ext := filepath.Ext(base)
	if noiseExt[ext] {
		return true
	}
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return true
	}
	return false
}

func filterUnimportantSessionFiles(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	out := make([]string, 0, len(files))
	seen := map[string]bool{}
	for _, file := range files {
		file = strings.TrimSpace(filepath.ToSlash(file))
		if file == "" || seen[file] || isUnimportantSessionFile(file) {
			continue
		}
		seen[file] = true
		out = append(out, file)
	}
	return out
}
