package memory

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	toolUsePattern  = regexp.MustCompile(`\[tool use: ([^\]]+)\]`)
	filePathPattern = regexp.MustCompile(`"(?:file_path|path)"\s*:\s*"([^"]+)"`)
	commandPattern  = regexp.MustCompile(`"command"\s*:\s*"([^"]+)"`)
)

type tracedToolCall struct {
	Name string
	Body string
}

func extractToolTraceMemories(input ExtractionInput) []MemoryJudgement {
	transcript := strings.TrimSpace(input.CompactedTranscript)
	if transcript == "" {
		transcript = strings.TrimSpace(input.Transcript)
	}
	if transcript == "" {
		return nil
	}
	calls := tracedToolCalls(transcript)
	tools := toolNames(calls)
	if len(tools) == 0 {
		tools = uniqueMatches(toolUsePattern, transcript)
	}
	commands := uniqueMatches(commandPattern, transcript)
	modifications := fileModificationSummaries(calls)
	paths := uniqueMatches(filePathPattern, transcript)

	judgements := make([]MemoryJudgement, 0, 4)
	if len(modifications) > 0 {
		judgements = append(judgements, MemoryJudgement{
			ShouldStore:   true,
			Kind:          KindTask,
			Scope:         ScopeProject,
			SuggestedTier: TierShortTerm,
			Confidence:    0.9,
			CanonicalText: "Compacted session file modifications: " + strings.Join(limitStrings(modifications, 12), "; ") + ".",
			Tags:          []string{"code-change", "files", "modifications", "compaction"},
			Reason:        "deterministic extraction of per-file modifications from compacted tool calls",
		})
	}
	if len(paths) > 0 && len(modifications) == 0 {
		judgements = append(judgements, MemoryJudgement{
			ShouldStore:   true,
			Kind:          KindTask,
			Scope:         ScopeProject,
			SuggestedTier: TierShortTerm,
			Confidence:    0.8,
			CanonicalText: "Compacted session touched files/paths without clear edit action: " + strings.Join(limitStrings(paths, 16), ", ") + ".",
			Tags:          []string{"code-change", "files", "compaction"},
			Reason:        "deterministic extraction of file path parameters from compacted transcript",
		})
	}
	validation := validationCommands(commands)
	if len(validation) > 0 {
		judgements = append(judgements, MemoryJudgement{
			ShouldStore:   true,
			Kind:          KindTask,
			Scope:         ScopeProject,
			SuggestedTier: TierShortTerm,
			Confidence:    0.85,
			CanonicalText: "Compacted session validation/build commands run: " + strings.Join(limitStrings(validation, 8), "; ") + ".",
			Tags:          []string{"validation", "build", "compaction"},
			Reason:        "deterministic extraction of validation commands from compacted transcript",
		})
	}
	if len(tools) > 0 {
		judgements = append(judgements, MemoryJudgement{
			ShouldStore:   true,
			Kind:          KindTask,
			Scope:         ScopeSession,
			SuggestedTier: TierWorking,
			Confidence:    0.65,
			CanonicalText: "Compacted session tools used: " + strings.Join(limitStrings(tools, 16), ", ") + ".",
			Tags:          []string{"tool-usage", "compaction"},
			Reason:        "low-priority deterministic extraction of tool names; file modification memories carry the durable trace",
		})
	}
	return judgements
}

func tracedToolCalls(transcript string) []tracedToolCall {
	var calls []tracedToolCall
	var current *tracedToolCall
	flush := func() {
		if current != nil {
			current.Body = strings.TrimSpace(current.Body)
			calls = append(calls, *current)
			current = nil
		}
	}
	for _, line := range strings.Split(transcript, "\n") {
		if match := toolUsePattern.FindStringSubmatch(line); len(match) >= 2 {
			flush()
			current = &tracedToolCall{Name: strings.TrimSpace(match[1])}
			continue
		}
		if current != nil {
			current.Body += line + "\n"
		}
	}
	flush()
	return calls
}

func toolNames(calls []tracedToolCall) []string {
	seen := map[string]bool{}
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name != "" {
			seen[name] = true
		}
	}
	return sortedKeys(seen)
}

func fileModificationSummaries(calls []tracedToolCall) []string {
	seen := map[string]bool{}
	var summaries []string
	for _, call := range calls {
		toolName := strings.TrimSpace(call.Name)
		fields := jsonFields(call.Body)
		paths := pathsForCall(fields, call.Body)
		if len(paths) == 0 {
			if summary := bashModificationSummary(fields["command"]); summary != "" && !seen[summary] {
				seen[summary] = true
				summaries = append(summaries, summary)
			}
			continue
		}
		for _, path := range paths {
			summary := modificationSummary(toolName, path, fields)
			if summary == "" || seen[summary] {
				continue
			}
			seen[summary] = true
			summaries = append(summaries, summary)
		}
	}
	sort.Strings(summaries)
	return summaries
}

func jsonFields(text string) map[string]string {
	fields := map[string]string{}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		var raw map[string]any
		if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err == nil {
			flattenJSONFields("", raw, fields)
		}
	}
	for _, key := range []string{"file_path", "path", "command", "old_string", "new_string", "patch_text"} {
		if fields[key] != "" {
			continue
		}
		pattern := regexp.MustCompile(fmt.Sprintf(`"%s"\s*:\s*"([^"]*)"`, regexp.QuoteMeta(key)))
		if match := pattern.FindStringSubmatch(text); len(match) >= 2 {
			fields[key] = strings.ReplaceAll(match[1], `\\`, `\`)
		}
	}
	return fields
}

func flattenJSONFields(prefix string, value any, out map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			flattenJSONFields(key, child, out)
		}
	case []any:
		for _, child := range typed {
			flattenJSONFields(prefix, child, out)
		}
	case string:
		if prefix != "" && out[prefix] == "" {
			out[prefix] = typed
		}
	case float64, bool:
		if prefix != "" && out[prefix] == "" {
			out[prefix] = fmt.Sprintf("%v", typed)
		}
	}
}

func pathsForCall(fields map[string]string, raw string) []string {
	seen := map[string]bool{}
	for _, key := range []string{"file_path", "path"} {
		if value := strings.TrimSpace(fields[key]); value != "" {
			seen[value] = true
		}
	}
	for _, value := range uniqueMatches(filePathPattern, raw) {
		seen[value] = true
	}
	return sortedKeys(seen)
}

func modificationSummary(toolName, path string, fields map[string]string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	lowerTool := strings.ToLower(toolName)
	switch {
	case strings.Contains(lowerTool, "edit"):
		return path + ": edited" + replacementDetail(fields)
	case strings.Contains(lowerTool, "write"):
		return path + ": wrote/overwrote file"
	case strings.Contains(lowerTool, "patch"):
		return path + ": applied patch" + patchDetail(fields)
	default:
		return path + ": modified via " + nonEmpty(toolName, "tool") + replacementDetail(fields)
	}
}

func replacementDetail(fields map[string]string) string {
	hasOld := strings.TrimSpace(fields["old_string"]) != ""
	hasNew := strings.TrimSpace(fields["new_string"]) != ""
	switch {
	case hasOld && hasNew:
		return " (targeted replacement)"
	case hasNew:
		return " (added content)"
	case hasOld:
		return " (removed content)"
	default:
		return ""
	}
}

func patchDetail(fields map[string]string) string {
	if strings.TrimSpace(fields["patch_text"]) == "" {
		return ""
	}
	return " (unified diff patch)"
}

func bashModificationSummary(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	lower := strings.ToLower(command)
	if strings.Contains(lower, "gofmt") && strings.Contains(lower, " -w") {
		return "formatted files via bash command: " + excerpt(command, 160)
	}
	return ""
}

func uniqueMatches(pattern *regexp.Regexp, text string) []string {
	seen := map[string]bool{}
	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		value := strings.TrimSpace(strings.ReplaceAll(match[1], `\\`, `\`))
		if value == "" {
			continue
		}
		seen[value] = true
	}
	return sortedKeys(seen)
}

func validationCommands(commands []string) []string {
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		lower := strings.ToLower(command)
		if strings.Contains(lower, "go test") || strings.Contains(lower, "go build") || strings.Contains(lower, "gofmt") || strings.Contains(lower, "npm test") || strings.Contains(lower, "npm run") || strings.Contains(lower, "pnpm test") || strings.Contains(lower, "pytest") {
			out = append(out, command)
		}
	}
	return out
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func excerpt(text string, maxRunes int) string {
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

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}
