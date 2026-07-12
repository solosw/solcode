package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const sideBySideDiffMinWidth = 60

// renderInlineDiff detects unified diff content. Wider terminals use a compact
// review layout with line-number gutters and old/new columns; narrow terminals
// retain the unified representation.
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

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		switch {
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			inDiff = true
			b.WriteString(t.DiffHdr.Bold(true).Render(truncateLine(line, contentWidth)) + "\n")
		case strings.HasPrefix(line, "@@"):
			inDiff = true
			b.WriteString(t.DiffHdr.Render(truncateLine(line, contentWidth)) + "\n")
		case !inDiff:
			continue
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			addedCount++
			b.WriteString(renderUnifiedDiffLine(line, diffLineAdd, t, contentWidth) + "\n")
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deletedCount++
			b.WriteString(renderUnifiedDiffLine(line, diffLineDel, t, contentWidth) + "\n")
		default:
			b.WriteString(t.DiffCtx.Render(truncateLine(line, contentWidth)) + "\n")
		}
	}
	if !inDiff {
		return ""
	}
	b.WriteString(renderDiffSummary(addedCount, deletedCount, t))
	return b.String()
}

type sideDiffRow struct {
	left, right         string
	leftKind, rightKind diffLineKind
	leftLine, rightLine int
	full                string
	fullKind            diffLineKind
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
	rows := make([]sideDiffRow, 0, len(lines))
	inDiff := false
	oldFile, newFile := "OLD", "NEW"
	oldLine, newLine := 0, 0
	addedCount, deletedCount := 0, 0

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "--- "):
			inDiff = true
			oldFile = strings.TrimSpace(strings.TrimPrefix(line, "--- "))
		case strings.HasPrefix(line, "+++ "):
			inDiff = true
			newFile = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			rows = append(rows, sideDiffRow{
				left: "--- " + oldFile, right: "+++ " + newFile,
				leftKind: diffLineHeader, rightKind: diffLineHeader,
			})
		case strings.HasPrefix(line, "@@"):
			inDiff = true
			oldLine, newLine = parseDiffHunkLines(line)
			rows = append(rows, sideDiffRow{full: line, fullKind: diffLineHeader})
		case !inDiff:
			if trimmed != "" && !strings.HasPrefix(trimmed, "<") {
				rows = append(rows, sideDiffRow{full: trimmed, fullKind: diffLineMeta})
			}
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deletedCount++
			rows = append(rows, sideDiffRow{left: line, leftKind: diffLineDel, leftLine: oldLine})
			oldLine++
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			addedCount++
			if len(rows) > 0 && rows[len(rows)-1].leftKind == diffLineDel && rows[len(rows)-1].right == "" && rows[len(rows)-1].full == "" {
				rows[len(rows)-1].right = line
				rows[len(rows)-1].rightKind = diffLineAdd
				rows[len(rows)-1].rightLine = newLine
			} else {
				rows = append(rows, sideDiffRow{right: line, rightKind: diffLineAdd, rightLine: newLine})
			}
			newLine++
		case strings.HasPrefix(line, " "):
			rows = append(rows, sideDiffRow{
				left: line, right: line,
				leftKind: diffLineContext, rightKind: diffLineContext,
				leftLine: oldLine, rightLine: newLine,
			})
			oldLine++
			newLine++
		case trimmed != "":
			rows = append(rows, sideDiffRow{
				left: line, right: line,
				leftKind: diffLineContext, rightKind: diffLineContext,
				leftLine: oldLine, rightLine: newLine,
			})
			oldLine++
			newLine++
		}
	}
	if len(rows) == 0 {
		return ""
	}

	separator := " │ "
	columnWidth := max(20, (contentWidth-lipgloss.Width(separator))/2)
	var b strings.Builder
	for _, row := range rows {
		if row.full != "" {
			b.WriteString(renderDiffFullRow(row.full, row.fullKind, t, contentWidth))
			b.WriteString("\n")
			continue
		}
		b.WriteString(renderDiffCell(row.left, row.leftLine, row.leftKind, t, columnWidth))
		b.WriteString(t.Dim.Render(separator))
		b.WriteString(renderDiffCell(row.right, row.rightLine, row.rightKind, t, columnWidth))
		b.WriteString("\n")
	}
	b.WriteString(renderDiffSummary(addedCount, deletedCount, t))
	return b.String()
}

func parseDiffHunkLines(header string) (int, int) {
	fields := strings.Fields(header)
	if len(fields) < 3 {
		return 0, 0
	}
	return parseDiffRange(fields[1]), parseDiffRange(fields[2])
}

func parseDiffRange(value string) int {
	value = strings.TrimLeft(value, "+-")
	if comma := strings.IndexByte(value, ','); comma >= 0 {
		value = value[:comma]
	}
	line, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return line
}

func renderUnifiedDiffLine(line string, kind diffLineKind, t Theme, width int) string {
	line = truncateLine(line, width)
	switch kind {
	case diffLineAdd:
		return t.DiffAdd.Background(diffBackground(t, kind)).Bold(true).Width(width).Render(line)
	case diffLineDel:
		return t.DiffDel.Background(diffBackground(t, kind)).Bold(true).Width(width).Render(line)
	default:
		return t.DiffCtx.Render(line)
	}
}

func renderDiffCell(line string, lineNumber int, kind diffLineKind, t Theme, width int) string {
	gutter := strings.Repeat(" ", 4)
	if lineNumber > 0 {
		gutter = padLeft(strconv.Itoa(lineNumber), 4)
	}
	content := truncateLine(" "+line, max(1, width-lipgloss.Width(gutter)))
	cell := gutter + content

	style := t.DiffCtx
	switch kind {
	case diffLineAdd:
		style = t.DiffAdd.Bold(true)
	case diffLineDel:
		style = t.DiffDel.Bold(true)
	case diffLineHeader:
		style = t.DiffHdr.Bold(true)
	}
	return style.Background(diffBackground(t, kind)).Width(width).Render(cell)
}

func renderDiffFullRow(line string, kind diffLineKind, t Theme, width int) string {
	line = truncateLine(line, width)
	switch kind {
	case diffLineHeader:
		return t.DiffHdr.Background(diffBackground(t, kind)).Bold(true).Width(width).Render(line)
	case diffLineMeta:
		return t.ToolResult.Width(width).Render(line)
	default:
		return t.DiffCtx.Width(width).Render(line)
	}
}

func diffBackground(t Theme, kind diffLineKind) lipgloss.Color {
	if t.Name == "light" {
		switch kind {
		case diffLineAdd:
			return lipgloss.Color("#dff5e5")
		case diffLineDel:
			return lipgloss.Color("#fbe1e5")
		case diffLineHeader:
			return lipgloss.Color("#e7edf7")
		default:
			return lipgloss.Color("#f3f3f3")
		}
	}
	switch kind {
	case diffLineAdd:
		return lipgloss.Color("#123b2a")
	case diffLineDel:
		return lipgloss.Color("#482128")
	case diffLineHeader:
		return lipgloss.Color("#202c42")
	default:
		return lipgloss.Color("#1a1a1a")
	}
}

func renderDiffSummary(addedCount, deletedCount int, t Theme) string {
	if addedCount == 0 && deletedCount == 0 {
		return "\n"
	}
	parts := []string{}
	if addedCount > 0 {
		parts = append(parts, t.DiffAdd.Bold(true).Render("+"+strconv.Itoa(addedCount)))
	}
	if deletedCount > 0 {
		parts = append(parts, t.DiffDel.Bold(true).Render("-"+strconv.Itoa(deletedCount)))
	}
	return t.Dim.Render(" changes ") + strings.Join(parts, "  ") + "\n"
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
	if lipgloss.Width(line) <= width {
		return line
	}
	var b strings.Builder
	for _, r := range line {
		if lipgloss.Width(b.String()+string(r)) >= width {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "…"
}

func padLeft(line string, width int) string {
	if lipgloss.Width(line) >= width {
		return line
	}
	return strings.Repeat(" ", width-lipgloss.Width(line)) + line
}

func padRight(line string, width int) string {
	if lipgloss.Width(line) >= width {
		return line
	}
	return line + strings.Repeat(" ", width-lipgloss.Width(line))
}
