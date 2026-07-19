package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GrepParams is the input schema for the grep tool.
type GrepParams struct {
	Pattern     string `json:"pattern"`
	Path        string `json:"path,omitempty"`
	Include     string `json:"include,omitempty"`
	LiteralText bool   `json:"literal_text,omitempty"`
}

const (
	GrepToolName   = "Grep"
	MaxGrepResults = 100
)

type grepMatch struct {
	path     string
	modTime  time.Time
	lineNum  int
	lineText string
}

type grepTool struct {
	BaseTool
}

// NewGrepTool creates a new content search tool.
func NewGrepTool() Tool {
	return &grepTool{}
}

func (g *grepTool) Name() string                             { return GrepToolName }
func (g *grepTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (g *grepTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (g *grepTool) Description() string {
	return `Fast content search tool that finds files containing specific text or patterns.
Returns matching file paths sorted by modification time (newest first).
- Supports regex patterns (set literal_text=true for exact text search).
- Optionally filter by file pattern (e.g. '*.go', '*.{ts,tsx}').
- Results limited to 100 matches.
- Use this instead of 'grep' or 'find ... | xargs grep'.`
}

func (g *grepTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The regex pattern to search for in file contents",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. Defaults to current working directory.",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "File pattern to include (e.g. '*.go', '*.{ts,tsx}')",
			},
			"literal_text": map[string]any{
				"type":        "boolean",
				"description": "If true, treat the pattern as literal text (escape regex special chars). Default false.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (g *grepTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params GrepParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if params.Pattern == "" {
		return ErrorResult("pattern is required"), nil
	}

	searchPattern := params.Pattern
	if params.LiteralText {
		searchPattern = regexp.QuoteMeta(params.Pattern)
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = uctx.WorkDir
	}
	if searchPath == "" {
		searchPath = "."
	}
	searchPath = ResolvePath(uctx, searchPath)
	if err := CheckAllowedPath(uctx, searchPath); err != nil {
		return ErrorResult(err.Error()), nil
	}

	matches, truncated, err := searchFiles(ctx, searchPattern, searchPath, params.Include, MaxGrepResults)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ErrorResult("search canceled"), nil
		}
		return ErrorResult(fmt.Sprintf("error searching: %v", err)), nil
	}

	var output string
	if len(matches) == 0 {
		output = "No matches found"
	} else {
		output = fmt.Sprintf("Found %d matches\n", len(matches))
		currentFile := ""
		for _, match := range matches {
			if currentFile != match.path {
				if currentFile != "" {
					output += "\n"
				}
				currentFile = match.path
				output += fmt.Sprintf("%s:\n", match.path)
			}
			output += fmt.Sprintf("  Line %d: %s\n", match.lineNum, match.lineText)
		}
		if truncated {
			output += "\n(Results are truncated. Consider using a more specific path or pattern.)"
		}
	}

	return Result(output), nil
}

func searchFiles(ctx context.Context, pattern, rootPath, include string, limit int) ([]grepMatch, bool, error) {
	// Try ripgrep first
	matches, err := searchWithRipgrep(ctx, pattern, rootPath, include)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, false, err
		}
		matches, err = searchWithRegex(ctx, pattern, rootPath, include)
		if err != nil {
			return nil, false, err
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime.After(matches[j].modTime)
	})

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}

	return matches, truncated, nil
}

func searchWithRipgrep(ctx context.Context, pattern, path, include string) ([]grepMatch, error) {
	_, err := exec.LookPath("rg")
	if err != nil {
		return nil, fmt.Errorf("ripgrep not found: %w", err)
	}

	args := []string{"-H", "-n", "--no-heading", pattern}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, path)

	cmd := exec.CommandContext(ctx, "rg", args...)
	output, err := cmd.Output()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []grepMatch{}, nil
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	matches := make([]grepMatch, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}

		filePath := parts[0]
		lineNum, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		lineText := parts[2]

		fileInfo, statErr := os.Stat(filePath)
		if statErr != nil {
			continue
		}

		matches = append(matches, grepMatch{
			path:     filePath,
			modTime:  fileInfo.ModTime(),
			lineNum:  lineNum,
			lineText: lineText,
		})
	}

	return matches, nil
}

func searchWithRegex(ctx context.Context, pattern, rootPath, include string) ([]grepMatch, error) {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var includeRegex *regexp.Regexp
	if include != "" {
		regexPattern := GlobToRegex(include)
		includeRegex, err = regexp.Compile(regexPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	var matches []grepMatch
	err = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if SkipHiddenPath(path) {
				return filepath.SkipDir
			}
			// skip common dirs
			switch base {
			case ".git", "node_modules", "__pycache__", "vendor", "dist", "build", "target":
				return filepath.SkipDir
			}
			return nil
		}

		if SkipHiddenPath(path) {
			return nil
		}

		if includeRegex != nil && !includeRegex.MatchString(path) {
			return nil
		}

		match, lineNum, lineText, err := fileContainsPattern(path, regex)
		if err != nil {
			return nil
		}
		if match {
			matches = append(matches, grepMatch{
				path:     path,
				modTime:  info.ModTime(),
				lineNum:  lineNum,
				lineText: lineText,
			})
			if len(matches) >= 200 {
				return filepath.SkipAll
			}
		}
		return nil
	})

	return matches, err
}

func fileContainsPattern(filePath string, pattern *regexp.Regexp) (bool, int, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return false, 0, "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if pattern.MatchString(line) {
			return true, lineNum, line, nil
		}
	}
	return false, 0, "", scanner.Err()
}

func GlobToRegex(glob string) string {
	pattern := strings.ReplaceAll(glob, ".", "\\.")
	pattern = strings.ReplaceAll(pattern, "*", ".*")
	pattern = strings.ReplaceAll(pattern, "?", ".")

	re := regexp.MustCompile(`\{([^}]+)\}`)
	pattern = re.ReplaceAllStringFunc(pattern, func(match string) string {
		inner := match[1 : len(match)-1]
		return "(" + strings.ReplaceAll(inner, ",", "|") + ")"
	})

	return pattern
}
