package tool

import (
	"context"
	"strings"
	"unicode/utf8"
)

const maxChangeDescriptionRunes = 30

func validateChangeDescription(description string) (string, string) {
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > maxChangeDescriptionRunes {
		return "", "desc must be at most 30 Chinese characters"
	}
	return description, ""
}

func recordFileChange(ctx context.Context, uctx *UseContext, toolName, path, description, before, after string) {
	if uctx == nil || uctx.RecordFileChange == nil || strings.TrimSpace(description) == "" || before == after {
		return
	}
	uctx.RecordFileChange(ctx, FileChange{ToolName: toolName, Path: path, Description: description, Before: before, After: after})
}
