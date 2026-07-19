package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LSParams is the input schema for the ls tool.
type LSParams struct {
	Path   string   `json:"path,omitempty"`
	Ignore []string `json:"ignore,omitempty"`
}

const (
	LSToolName = "LS"
	MaxLSFiles = 1000
)

type lsTool struct {
	BaseTool
}

// NewLsTool creates a new directory listing tool.
func NewLsTool() Tool {
	return &lsTool{}
}

func (l *lsTool) Name() string                             { return LSToolName }
func (l *lsTool) Aliases() []string                        { return []string{"List"} }
func (l *lsTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (l *lsTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (l *lsTool) Description() string {
	return `Directory listing tool that shows files and subdirectories in a tree structure.
- Provide a path to list (defaults to current working directory).
- Optionally specify glob patterns to ignore.
- Results displayed in a tree structure.
- Skips hidden files and common system directories.
- Results limited to 1000 entries.`
}

func (l *lsTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The path to the directory to list",
			},
			"ignore": map[string]any{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "List of glob patterns to ignore",
			},
		},
		"required": []string{},
	}
}

type treeNode struct {
	Name     string
	Path     string
	IsDir    bool
	Children []*treeNode
}

func (l *lsTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params LSParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
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

	if _, err := os.Stat(searchPath); os.IsNotExist(err) {
		return ErrorResult(fmt.Sprintf("path does not exist: %s", searchPath)), nil
	}

	files, truncated, err := ListDirectory(ctx, searchPath, params.Ignore, MaxLSFiles)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ErrorResult("directory listing canceled"), nil
		}
		return ErrorResult(fmt.Sprintf("error listing directory: %v", err)), nil
	}

	output := printDirectoryTree(files, searchPath)

	if truncated {
		output = fmt.Sprintf("More than %d files in the directory. Use a more specific path or Glob.\nThe first %d entries:\n\n%s",
			MaxLSFiles, MaxLSFiles, output)
	}

	return Result(output), nil
}

func ListDirectory(ctx context.Context, rootPath string, ignorePatterns []string, limit int) ([]string, bool, error) {
	var results []string
	truncated := false

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}

		if path == rootPath {
			return nil
		}

		if ShouldSkipLS(path, ignorePatterns) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		entry := path
		if info.IsDir() {
			entry = path + string(filepath.Separator)
		}
		results = append(results, entry)

		if len(results) >= limit {
			truncated = true
			return filepath.SkipAll
		}

		return nil
	})

	sort.Strings(results)
	return results, truncated, err
}

func ShouldSkipLS(path string, ignorePatterns []string) bool {
	base := filepath.Base(path)

	// Skip hidden files
	if base != "." && strings.HasPrefix(base, ".") {
		return true
	}

	// Common ignored directories
	commonIgnored := []string{
		"__pycache__", "node_modules", "dist", "build", "target",
		"vendor", "bin", "obj", ".git", ".idea", ".vscode", ".DS_Store",
	}

	for _, ignored := range commonIgnored {
		if base == ignored {
			return true
		}
	}

	// Skip binary-ish extensions
	skipExt := map[string]bool{
		".pyc": true, ".pyo": true, ".pyd": true, ".so": true,
		".dll": true, ".exe": true, ".o": true, ".a": true,
	}
	ext := strings.ToLower(filepath.Ext(base))
	if skipExt[ext] {
		return true
	}

	// User-provided ignore patterns
	for _, pattern := range ignorePatterns {
		matched, err := filepath.Match(pattern, base)
		if err == nil && matched {
			return true
		}
	}

	return false
}

func printDirectoryTree(paths []string, rootPath string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("- %s%s\n", rootPath, string(filepath.Separator)))

	// Build tree nodes
	children := buildTreeNodes(paths, rootPath)

	for _, child := range children {
		printTreeEntry(&sb, child, 1)
	}

	return sb.String()
}

func buildTreeNodes(paths []string, rootPath string) []*treeNode {
	root := make([]*treeNode, 0)
	dirMap := make(map[string]*treeNode)

	for _, p := range paths {
		relPath := strings.TrimPrefix(p, rootPath)
		relPath = strings.TrimPrefix(relPath, string(filepath.Separator))
		parts := strings.Split(relPath, string(filepath.Separator))

		var cleanParts []string
		for _, part := range parts {
			if part != "" {
				cleanParts = append(cleanParts, part)
			}
		}
		parts = cleanParts
		if len(parts) == 0 {
			continue
		}

		currentPath := ""
		for i, part := range parts {
			if currentPath == "" {
				currentPath = part
			} else {
				currentPath = filepath.Join(currentPath, part)
			}

			if _, exists := dirMap[currentPath]; exists {
				continue
			}

			isLast := i == len(parts)-1
			isDir := !isLast || strings.HasSuffix(p, string(filepath.Separator))
			node := &treeNode{
				Name:  part,
				Path:  filepath.Join(rootPath, currentPath),
				IsDir: isDir,
			}

			dirMap[currentPath] = node

			if i == 0 {
				root = append(root, node)
			} else {
				parentPath := filepath.Join(parts[:i]...)
				if parent, ok := dirMap[parentPath]; ok {
					parent.Children = append(parent.Children, node)
				}
			}
		}
	}

	return root
}

func printTreeEntry(sb *strings.Builder, node *treeNode, depth int) {
	indent := strings.Repeat("  ", depth)
	name := node.Name
	if node.IsDir {
		name += string(filepath.Separator)
	}
	fmt.Fprintf(sb, "%s- %s\n", indent, name)

	for _, child := range node.Children {
		printTreeEntry(sb, child, depth+1)
	}
}
