package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

func toolInputSummary(toolName, input string, width int) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(input), &fields); err == nil {
		if summary := commonToolSummary(toolName, fields); summary != "" {
			return truncate(summary, max(20, width))
		}
	}
	return truncate(oneLine(input), max(20, width))
}

func commonToolSummary(toolName string, fields map[string]any) string {
	keys := []string{"command", "file_path", "path", "url", "pattern", "query"}
	for _, key := range keys {
		if value := fieldString(fields, key); value != "" {
			return value
		}
	}
	lowerName := strings.ToLower(toolName)
	if strings.Contains(lowerName, "bash") {
		return fieldString(fields, "cmd")
	}
	return ""
}

func fieldString(fields map[string]any, key string) string {
	value, ok := fields[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return oneLine(typed)
	case []any:
		if len(typed) == 0 {
			return ""
		}
		return oneLine(fmt.Sprint(typed[0]))
	default:
		return oneLine(fmt.Sprint(typed))
	}
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func isFileMutationTool(toolName string) bool {
	name := strings.ToLower(strings.TrimSpace(toolName))
	name = strings.TrimPrefix(name, "functions.")
	switch name {
	case "edit", "write", "patch", "diff",
		"mcp__filesystem__edit-file", "mcp__filesystem__write-file":
		return true
	}
	return strings.Contains(name, "edit-file") || strings.Contains(name, "write-file") || strings.HasSuffix(name, ".edit") || strings.HasSuffix(name, ".write") || strings.HasSuffix(name, ".patch")
}
