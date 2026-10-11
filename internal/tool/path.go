package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolvePath resolves a user-provided path for tool use.
//
// Resolution order (Agent Skills-compatible):
//  1. Absolute paths are cleaned and returned as-is.
//  2. Paths that look like skill package relatives (scripts/, references/, assets/)
//     are resolved against UseContext.SkillRoots first, then WorkDir.
//  3. Other relative paths prefer WorkDir, then skill roots if the workdir target
//     does not exist but a skill-root target does.
//  4. If nothing exists yet, fall back to WorkDir join (create/write paths).
func ResolvePath(uctx *UseContext, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	path = filepath.Clean(path)
	if filepath.IsAbs(path) {
		return path
	}

	workDir := ""
	var roots []string
	if uctx != nil {
		workDir = strings.TrimSpace(uctx.WorkDir)
		roots = uctx.SkillRoots
	}

	workCandidate := ""
	if workDir != "" {
		workCandidate = filepath.Join(workDir, path)
	}

	skillCandidates := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		skillCandidates = append(skillCandidates, filepath.Join(root, path))
	}

	// Prefer skill roots for package-relative layout paths.
	if looksLikeSkillRelative(path) {
		for _, c := range skillCandidates {
			if pathExists(c) {
				return filepath.Clean(c)
			}
		}
		if workCandidate != "" && pathExists(workCandidate) {
			return filepath.Clean(workCandidate)
		}
		// Prefer first skill root even if missing (read errors still point at skill).
		if len(skillCandidates) > 0 {
			return filepath.Clean(skillCandidates[0])
		}
		if workCandidate != "" {
			return filepath.Clean(workCandidate)
		}
		return path
	}

	if workCandidate != "" && pathExists(workCandidate) {
		return filepath.Clean(workCandidate)
	}
	for _, c := range skillCandidates {
		if pathExists(c) {
			return filepath.Clean(c)
		}
	}
	if workCandidate != "" {
		return filepath.Clean(workCandidate)
	}
	if len(skillCandidates) > 0 {
		return filepath.Clean(skillCandidates[0])
	}
	return path
}

// CheckAllowedPath accepts any filesystem path. Tools are not confined to
// WorkDir, ~/.solcode, or skill roots. Relative paths are still resolved by
// ResolvePath before this check.
func CheckAllowedPath(_ *UseContext, target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("path is empty")
	}
	return nil
}

func looksLikeSkillRelative(path string) bool {
	slash := filepath.ToSlash(path)
	slash = strings.TrimPrefix(slash, "./")
	switch {
	case slash == "scripts", slash == "references", slash == "assets":
		return true
	case strings.HasPrefix(slash, "scripts/"),
		strings.HasPrefix(slash, "references/"),
		strings.HasPrefix(slash, "assets/"):
		return true
	case strings.EqualFold(slash, "skill.md"):
		return true
	default:
		return false
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
