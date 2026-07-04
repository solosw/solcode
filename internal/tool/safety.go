package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// BaseTool provides default implementations for optional Tool methods.
// Embed *BaseTool in your tool struct to satisfy the interface with
// sensible defaults, then override only what you need.
//
//	type MyTool struct {
//	    tool.BaseTool
//	    // ... fields
//	}
type BaseTool struct{}

func (b *BaseTool) IsDestructive(_ json.RawMessage) bool { return false }
func (b *BaseTool) IsReadOnly(_ json.RawMessage) bool    { return false }
func (b *BaseTool) IsConcurrencySafe(_ json.RawMessage) bool {
	return false
}
func (b *BaseTool) Aliases() []string { return nil }
func (b *BaseTool) ValidateInput(_ context.Context, _ json.RawMessage) error {
	return nil
}

// CheckWithinWorkDir returns an error if target is not within workDir.
// Both paths must be absolute. An empty workDir is treated as the current directory.
func CheckWithinWorkDir(workDir, target string) error {
	if workDir == "" {
		workDir = "."
	}
	workDir = filepath.Clean(workDir)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(workDir, target)
	if err != nil {
		return fmt.Errorf("cannot resolve path relative to work directory: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("path %s is outside the working directory", target)
	}
	return nil
}
