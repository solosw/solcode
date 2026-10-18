package app

import (
	"regexp"
	"strings"
)

var summaryPaginationLine = regexp.MustCompile(`^\(File has \d+ more lines\. Use 'offset' parameter to read beyond line \d+\)$`)

// sanitizeHandoffSummary keeps the handoff's structure and engineering evidence.
// Legacy cleanup intentionally drops procedural/code-like lines, which would
// erase next steps, inline symbols and validation evidence from this format.
func sanitizeHandoffSummary(summary string) (string, bool) {
	headings := [...]string{
		"## Active Request and Intent",
		"## Constraints and Decisions",
		"## Progress and Current State",
		"## Files and Symbols",
		"## Errors and Verification",
		"## Open Tasks and Blockers",
		"## Current Work",
		"## Next Step",
	}
	lines := strings.Split(strings.TrimSpace(summary), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != headings[0] {
		return "", false
	}
	out := make([]string, 0, len(lines))
	next := 0
	fence := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if marker == fence {
				fence = ""
			}
		} else if fence == "" {
			if strings.HasPrefix(trimmed, "## ") {
				if next >= len(headings) || trimmed != headings[next] {
					return "", false
				}
				next++
			}
			if summaryPaginationLine.MatchString(trimmed) {
				continue
			}
		}
		out = append(out, line)
	}
	if next != len(headings) || fence != "" {
		return "", false
	}
	return strings.Join(out, "\n"), true
}
