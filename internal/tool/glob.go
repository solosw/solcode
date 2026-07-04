package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// GlobParams is the input schema for the glob tool.
type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

const (
	GlobToolName   = "Glob"
	MaxGlobResults = 100
)

type globTool struct {
	BaseTool
}

// NewGlobTool creates a new file pattern matching tool.
func NewGlobTool() Tool {
	return &globTool{}
}

func (g *globTool) Name() string                        { return GlobToolName }
func (g *globTool) IsReadOnly(_ json.RawMessage) bool    { return true }
func (g *globTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (g *globTool) Description() string {
	return `Fast file pattern matching tool that finds files by name/pattern.
Returns matching paths sorted by modification time (newest first).
- Supports *, **, ?, and [...] glob syntax.
- Results limited to 100 files.
- Hidden files (starting with '.') are skipped.
- Use this instead of 'find' or 'ls' for finding files by pattern.`
}

func (g *globTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The glob pattern to match files against (e.g. '**/*.go')",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. Defaults to current working directory.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (g *globTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params GlobParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if params.Pattern == "" {
		return ErrorResult("pattern is required"), nil
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = uctx.WorkDir
	}
	if searchPath == "" {
		searchPath = "."
	}
	if !filepath.IsAbs(searchPath) {
		searchPath = filepath.Join(uctx.WorkDir, searchPath)
	}
	if err := CheckWithinWorkDir(uctx.WorkDir, searchPath); err != nil {
		return ErrorResult(err.Error()), nil
	}

	files, truncated, err := globFiles(ctx, params.Pattern, searchPath, MaxGlobResults)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ErrorResult("glob canceled"), nil
		}
		return ErrorResult(fmt.Sprintf("error finding files: %v", err)), nil
	}

	var output string
	if len(files) == 0 {
		output = "No files found"
	} else {
		output = strings.Join(files, "\n")
		if truncated {
			output += "\n\n(Results are truncated. Consider using a more specific path or pattern.)"
		}
	}

	return Result(output), nil
}

// globFiles finds files matching the pattern in the given directory.
func globFiles(ctx context.Context, pattern, searchPath string, limit int) ([]string, bool, error) {
	matches, _, err := globWithRipgrep(ctx, pattern, searchPath, limit)
	if err == nil {
		return matches, len(matches) >= limit, nil
	}
	if errors.Is(err, context.Canceled) {
		return nil, false, err
	}
	return GlobWithDoublestar(pattern, searchPath, limit)
}

func globWithRipgrep(ctx context.Context, pattern, searchPath string, limit int) ([]string, bool, error) {
	_, err := exec.LookPath("rg")
	if err != nil {
		return nil, false, err
	}

	cmd := exec.CommandContext(ctx, "rg", "--files", "--glob", pattern, searchPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, false, err
		}
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, false, nil
		}
		return nil, false, err
	}

	var matches []string
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p == "" {
			continue
		}
		if SkipHiddenPath(p) {
			continue
		}
		matches = append(matches, p)
	}

	sort.Strings(matches)
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, false, nil
}

func GlobWithDoublestar(pattern, searchPath string, limit int) ([]string, bool, error) {
	absPattern := filepath.Join(searchPath, pattern)
	matches, err := doublestar.FilepathGlob(absPattern)
	if err != nil {
		return nil, false, err
	}

	var filtered []string
	for _, m := range matches {
		if SkipHiddenPath(m) {
			continue
		}
		filtered = append(filtered, m)
	}

	sortByModTime(filtered)

	truncated := len(filtered) > limit && limit > 0
	if truncated {
		filtered = filtered[:limit]
	}
	return filtered, truncated, nil
}

func SkipHiddenPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

func sortByModTime(files []string) {
	sort.SliceStable(files, func(i, j int) bool {
		infoI, errI := os.Stat(files[i])
		infoJ, errJ := os.Stat(files[j])
		if errI != nil || errJ != nil {
			return files[i] < files[j]
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})
}
