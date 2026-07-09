package tui

import "strings"

const sideBySideDiffMinWidth = 60

// renderInlineDiff detects diff content within output and renders it with colors.
// Defaults to a side-by-side diff when there is enough room, and falls back to
// a unified inline diff on narrow terminals.
func renderInlineDiff(output string, t Theme, width int) string {
	if !containsDiffMarkers(output) {
		return ""
	}
	contentWidth := max(40, width-4)
	if contentWidth >= sideBySideDiffMinWidth {
		if sideBySide := renderSideBySideDiff(output, t, contentWidth); sideBySide != "" {
			return sideBySide
		}
	}
	return renderUnifiedInlineDiff(output, t, contentWidth)
}

func renderUnifiedInlineDiff(output string, t Theme, contentWidth int) string {
	var b strings.Builder
	lines := strings.Split(output, "\n")
	inDiff := false
	addedCount := 0
	deletedCount := 0

	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")

		// Detect diff header
		if strings.HasPrefix(trimmed, "--- ") || strings.HasPrefix(trimmed, "+++ ") {
			if !inDiff {
				inDiff = true
				b.WriteString(t.Dim.Render("│") + "\n")
			}
			b.WriteString(t.DiffHdr.Render(truncateLine(trimmed, contentWidth)) + "\n")
			continue
		}
		if strings.HasPrefix(trimmed, "@@") {
			if !inDiff {
				inDiff = true
			}
			b.WriteString(t.Dim.Render("│") + "\n")
			b.WriteString(t.DiffHdr.Render(truncateLine(trimmed, contentWidth)) + "\n")
			continue
		}
		if !inDiff {
			continue
		}

		if strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+++") {
			addedCount++
			b.WriteString(t.DiffAdd.Bold(true).Render(truncateLine(trimmed, contentWidth)) + "\n")
		} else if strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---") {
			deletedCount++
			b.WriteString(t.DiffDel.Bold(true).Render(truncateLine(trimmed, contentWidth)) + "\n")
		} else if strings.HasPrefix(trimmed, " ") || trimmed == "" {
			b.WriteString(t.Dim.Render(truncateLine(trimmed, contentWidth)) + "\n")
		} else {
			// context line without prefix
			b.WriteString(t.Dim.Render(truncateLine(trimmed, contentWidth)) + "\n")
		}
	}

	if inDiff {
		b.WriteString(renderDiffSummary(addedCount, deletedCount, t))
		return b.String()
	}
	return ""
}

type sideDiffRow struct {
	left      string
	right     string
	leftKind  diffLineKind
	rightKind diffLineKind
	full      string
	fullKind  diffLineKind
}

type diffLineKind int

const (
	diffLineContext diffLineKind = iota
	diffLineAdd
	diffLineDel
	diffLineHeader
	diffLineMeta
)

func renderSideBySideDiff(output string, t Theme, contentWidth int) string {
	lines := strings.Split(output, "\n")
	var rows []sideDiffRow
	inDiff := false
	oldFile := "OLD"
	newFile := "NEW"
	addedCount := 0
	deletedCount := 0

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "--- ") {
			inDiff = true
			oldFile = strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			inDiff = true
			newFile = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			rows = append(rows, sideDiffRow{left: "--- " + oldFile, right: "+++ " + newFile, leftKind: diffLineHeader, rightKind: diffLineHeader})
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inDiff = true
			rows = append(rows, sideDiffRow{full: line, fullKind: diffLineHeader})
			continue
		}
		if !inDiff {
			if trimmed != "" && !strings.HasPrefix(trimmed, "<") {
				rows = append(rows, sideDiffRow{full: trimmed, fullKind: diffLineMeta})
			}
			continue
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			deletedCount++
			rows = append(rows, sideDiffRow{left: line, leftKind: diffLineDel})
			continue
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			addedCount++
			if len(rows) > 0 && rows[len(rows)-1].leftKind == diffLineDel && rows[len(rows)-1].right == "" && rows[len(rows)-1].full == "" {
				rows[len(rows)-1].right = line
				rows[len(rows)-1].rightKind = diffLineAdd
			} else {
				rows = append(rows, sideDiffRow{right: line, rightKind: diffLineAdd})
			}
			continue
		}
		if strings.HasPrefix(line, " ") {
			rows = append(rows, sideDiffRow{left: line, right: line, leftKind: diffLineContext, rightKind: diffLineContext})
			continue
		}
		if trimmed != "" {
			rows = append(rows, sideDiffRow{left: line, right: line, leftKind: diffLineContext, rightKind: diffLineContext})
		}
	}

	if len(rows) == 0 {
		return ""
	}

	sep := " │ "
	colWidth := max(10, (contentWidth-len(sep))/2)
	var b strings.Builder
	for _, row := range rows {
		if row.full != "" {
			b.WriteString(renderDiffLine(row.full, row.fullKind, t, contentWidth))
			b.WriteString("\n")
			continue
		}
		left := padRight(truncateLine(row.left, colWidth), colWidth)
		right := padRight(truncateLine(row.right, colWidth), colWidth)
		b.WriteString(renderDiffLine(left, row.leftKind, t, colWidth))
		b.WriteString(t.Dim.Render(sep))
		b.WriteString(renderDiffLine(right, row.rightKind, t, colWidth))
		b.WriteString("\n")
	}
	b.WriteString(renderDiffSummary(addedCount, deletedCount, t))
	return b.String()
}

func renderDiffLine(line string, kind diffLineKind, t Theme, width int) string {
	line = truncateLine(line, width)
	switch kind {
	case diffLineAdd:
		return t.DiffAdd.Bold(true).Render(line)
	case diffLineDel:
		return t.DiffDel.Bold(true).Render(line)
	case diffLineHeader:
		return t.DiffHdr.Render(line)
	case diffLineMeta:
		return t.ToolResult.Render(line)
	default:
		return t.Dim.Render(line)
	}
}

func renderDiffSummary(addedCount, deletedCount int, t Theme) string {
	if addedCount == 0 && deletedCount == 0 {
		return "\n"
	}
	parts := []string{}
	if addedCount > 0 {
		parts = append(parts, t.DiffAdd.Render("+"+itoa(addedCount)))
	}
	if deletedCount > 0 {
		parts = append(parts, t.DiffDel.Render("-"+itoa(deletedCount)))
	}
	return t.Dim.Render(" ── ") + strings.Join(parts, " ") + "\n"
}

// containsDiffMarkers checks if output contains unified diff markers.
func containsDiffMarkers(output string) bool {
	hasHeader := strings.Contains(output, "\n--- ") || strings.HasPrefix(output, "--- ")
	hasRange := strings.Contains(output, "\n@@")
	hasChange := strings.Contains(output, "\n+") || strings.Contains(output, "\n-")
	return hasHeader && (hasRange || hasChange)
}

// isDiffOutput checks if the entire output appears to be a unified diff.
func isDiffOutput(output string) bool {
	if !containsDiffMarkers(output) {
		return false
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	diffLineCount := 0
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") ||
			strings.HasPrefix(trimmed, "@@") || strings.HasPrefix(trimmed, "---") ||
			strings.HasPrefix(trimmed, "+++") {
			diffLineCount++
		}
	}
	return diffLineCount >= len(lines)/3
}

func truncateLine(line string, width int) string {
	if width <= 1 {
		return "…"
	}
	if len(line) <= width {
		return line
	}
	return line[:width-1] + "…"
}

func padRight(line string, width int) string {
	if len(line) >= width {
		return line
	}
	return line + strings.Repeat(" ", width-len(line))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	digits := make([]byte, 0, 10)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return sign + string(digits)
}
