package memory

import (
	"regexp"
	"strings"
)

var (
	storedLineNumberPattern = regexp.MustCompile(`^(?:\d+\||line\s+\d+:|\d+\.\s+)`)
	storedCodeLinePattern   = regexp.MustCompile(`^(?:var|func|if|for|switch|case|return|type|const)\b`)
)

func sanitizeStoredMemoryItem(item Item) (Item, bool, bool) {
	cleaned := sanitizeStoredMemoryText(item.Text, item.Tags)
	if cleaned == "" {
		return item, false, false
	}
	if cleaned == strings.TrimSpace(item.Text) {
		return item, false, true
	}
	item.Text = cleaned
	return item, true, true
}

func sanitizeStoredMemoryText(text string, tags []string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if isStoredToolUsageMemory(tags, text) {
		return ""
	}
	lower := strings.ToLower(text)
	if idx := strings.Index(lower, "compacted session file modifications:"); idx >= 0 {
		rest := strings.TrimSpace(text[idx+len("compacted session file modifications:"):])
		parts := strings.Split(rest, ";")
		cleaned := make([]string, 0, len(parts))
		for _, part := range parts {
			part = sanitizeStoredModification(part)
			if part != "" {
				cleaned = append(cleaned, part)
			}
		}
		cleaned = dedupeStoredLines(cleaned)
		if len(cleaned) == 0 {
			return "Compacted session file modifications."
		}
		if len(cleaned) > 6 {
			cleaned = cleaned[:6]
		}
		return "Compacted session file modifications: " + strings.Join(cleaned, "; ") + "."
	}
	if idx := strings.Index(lower, "compacted session validation/build commands run:"); idx >= 0 {
		rest := strings.TrimSpace(text[idx+len("compacted session validation/build commands run:"):])
		parts := strings.Split(rest, ";")
		cleaned := make([]string, 0, len(parts))
		for _, part := range parts {
			part = storedExcerpt(strings.TrimSpace(strings.TrimSuffix(part, ".")), 120)
			if part != "" {
				cleaned = append(cleaned, part)
			}
		}
		cleaned = dedupeStoredLines(cleaned)
		if len(cleaned) == 0 {
			return "Compacted session validation/build commands run."
		}
		if len(cleaned) > 4 {
			cleaned = cleaned[:4]
		}
		return "Compacted session validation/build commands run: " + strings.Join(cleaned, "; ") + "."
	}
	lines := strings.Split(text, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "+ ")
		line = strings.TrimSpace(line)
		if line == "" || isNoisyStoredLine(line) || lineLooksLikeCode(line) {
			continue
		}
		cleaned = append(cleaned, line)
	}
	cleaned = dedupeStoredLines(cleaned)
	if len(cleaned) == 0 {
		if isNoisyStoredLine(text) || lineLooksLikeCode(text) {
			return ""
		}
		return storedExcerpt(text, 180)
	}
	if len(cleaned) == 1 {
		return storedExcerpt(cleaned[0], 180)
	}
	return strings.Join(cleaned, "\n")
}

func isStoredToolUsageMemory(tags []string, text string) bool {
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), "tool-usage") {
			return true
		}
	}
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "compacted session tools used:") || strings.Contains(lower, "compacted session tool usage:")
}

func sanitizeStoredModification(part string) string {
	part = strings.TrimSpace(strings.TrimSuffix(part, "."))
	if part == "" {
		return ""
	}
	replacements := []struct {
		old string
		new string
	}{
		{" (replaced ", " (targeted replacement)"},
		{" (new content includes ", " (added content)"},
	}
	for _, replacement := range replacements {
		if idx := strings.Index(part, replacement.old); idx >= 0 {
			part = part[:idx] + replacement.new
			return storedExcerpt(part, 140)
		}
	}
	stableSuffixes := []string{" (targeted replacement)", " (added content)", " (removed content)", " (unified diff patch)"}
	for _, suffix := range stableSuffixes {
		if strings.HasSuffix(part, suffix) {
			return storedExcerpt(part, 140)
		}
	}
	if idx := strings.Index(part, " ("); idx >= 0 {
		part = part[:idx]
	}
	return storedExcerpt(part, 140)
}

func isNoisyStoredLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true
	}
	lower := strings.ToLower(line)
	if storedLineNumberPattern.MatchString(lower) || strings.HasPrefix(line, "```") {
		return true
	}
	noiseMarkers := []string{
		"session summary:",
		"retrieved memory:",
		"current todos:",
		"tool call preserved as summarized metadata",
		"[tool use:",
		"[tool result]",
		"\"old_string\"",
		"\"new_string\"",
		"\"patch_text\"",
		"\"tool_id\"",
		"todos.json",
		"assistant:",
		"user:",
	}
	for _, marker := range noiseMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func lineLooksLikeCode(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if storedCodeLinePattern.MatchString(line) {
		return true
	}
	codeMarkers := []string{"{", "}", ":=", "strings.", "sdk.", "func(", "[]", "return ", "\t"}
	for _, marker := range codeMarkers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

func dedupeStoredLines(lines []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

func storedExcerpt(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}
