package changegraph

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/solosw/solcode/internal/codegraph"
)

const MaxDescriptionRunes = 30

type FileChange struct {
	SessionID   string
	ToolName    string
	Path        string
	Description string
	Before      string
	After       string
}

func ValidateDescription(description string) (string, error) {
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > MaxDescriptionRunes {
		return "", fmt.Errorf("desc must be at most %d Chinese characters", MaxDescriptionRunes)
	}
	return description, nil
}

// RecordFileChange records a successful described mutation. Unsupported files
// are retained as file-level changes without symbols.
func (s *Store) RecordFileChange(ctx context.Context, change FileChange) error {
	description, err := ValidateDescription(change.Description)
	if err != nil || description == "" || change.Before == change.After {
		return err
	}
	path := filepath.ToSlash(strings.TrimSpace(change.Path))
	if path == "" {
		return nil
	}
	beforeLines, afterLines := changedLineRangesForVersions(change.Before, change.After)
	displayLines := afterLines
	if len(displayLines) == 0 {
		displayLines = beforeLines
	}
	record := Change{
		SessionID:    change.SessionID,
		ToolName:     change.ToolName,
		Path:         path,
		Description:  description,
		ChangedLines: formatRanges(displayLines),
	}
	record.Symbols, record.Language = symbolsForChange(change.Before, change.After, path, beforeLines, afterLines)
	return s.Record(ctx, record)
}

type lineRange struct{ start, end int }

// changedLineRanges is retained for callers that need locations in the new
// file. Use changedLineRangesForVersions when old-file deletion locations are
// also needed.
func changedLineRanges(before, after string) []lineRange {
	_, afterRanges := changedLineRangesForVersions(before, after)
	return afterRanges
}

func changedLineRangesForVersions(before, after string) (beforeRanges, afterRanges []lineRange) {
	dmp := diffmatchpatch.New()
	beforeChars, afterChars, lineArray := dmp.DiffLinesToChars(before, after)
	diffs := dmp.DiffCharsToLines(dmp.DiffMain(beforeChars, afterChars, false), lineArray)
	beforeLine, afterLine := 1, 1
	for _, diff := range diffs {
		count := lineCount(diff.Text)
		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			beforeLine += count
			afterLine += count
		case diffmatchpatch.DiffDelete:
			if count > 0 {
				beforeRanges = append(beforeRanges, lineRange{start: beforeLine, end: beforeLine + count - 1})
				beforeLine += count
			}
		case diffmatchpatch.DiffInsert:
			if count > 0 {
				afterRanges = append(afterRanges, lineRange{start: afterLine, end: afterLine + count - 1})
				afterLine += count
			}
		}
	}
	return mergeRanges(beforeRanges), mergeRanges(afterRanges)
}

func symbolsForChange(before, after, path string, beforeLines, afterLines []lineRange) ([]Symbol, string) {
	builder := codegraph.NewGraphBuilder()
	beforeGraph, beforeErr := builder.Build([]byte(before), path)
	afterGraph, afterErr := builder.Build([]byte(after), path)
	language := ""
	if afterErr == nil {
		language = afterGraph.Language
	} else if beforeErr == nil {
		language = beforeGraph.Language
	}

	beforeSymbols := affectedSymbols(beforeGraph, beforeErr, beforeLines)
	afterSymbols := affectedSymbols(afterGraph, afterErr, afterLines)
	keys := make(map[string]struct{}, len(beforeSymbols)+len(afterSymbols))
	for key := range beforeSymbols {
		keys[key] = struct{}{}
	}
	for key := range afterSymbols {
		keys[key] = struct{}{}
	}
	orderedKeys := make([]string, 0, len(keys))
	for key := range keys {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)

	symbols := make([]Symbol, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		beforeSymbol, wasBefore := beforeSymbols[key]
		afterSymbol, isAfter := afterSymbols[key]
		switch {
		case wasBefore && isAfter:
			afterSymbol.ChangeKind = SymbolModified
			symbols = append(symbols, afterSymbol)
		case isAfter:
			afterSymbol.ChangeKind = SymbolAdded
			symbols = append(symbols, afterSymbol)
		default:
			beforeSymbol.ChangeKind = SymbolDeleted
			symbols = append(symbols, beforeSymbol)
		}
	}
	return symbols, language
}

func affectedSymbols(graph *codegraph.Graph, buildErr error, lines []lineRange) map[string]Symbol {
	out := make(map[string]Symbol)
	if buildErr != nil || graph == nil {
		return out
	}
	for _, node := range graph.Nodes {
		if node == nil || node.Kind == codegraph.KindFile || !overlaps(lines, node.Line, node.EndLine) {
			continue
		}
		symbol := Symbol{
			Kind:      string(node.Kind),
			Name:      node.Name,
			FullName:  node.FullName,
			StartLine: node.Line,
			EndLine:   node.EndLine,
		}
		if symbol.Name == "" {
			continue
		}
		out[symbolIdentity(symbol)] = symbol
	}
	return out
}

func symbolIdentity(symbol Symbol) string {
	name := symbol.FullName
	if name == "" {
		name = symbol.Name
	}
	return symbol.Kind + "\x00" + name
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	count := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		count++
	}
	return count
}

func mergeRanges(ranges []lineRange) []lineRange {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	out := []lineRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &out[len(out)-1]
		if r.start <= last.end+1 {
			if r.end > last.end {
				last.end = r.end
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func formatRanges(ranges []lineRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.start == r.end {
			parts = append(parts, fmt.Sprint(r.start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", r.start, r.end))
		}
	}
	return strings.Join(parts, ",")
}

func overlaps(ranges []lineRange, start, end int) bool {
	if end <= 0 {
		end = start
	}
	for _, r := range ranges {
		if start <= r.end && end >= r.start {
			return true
		}
	}
	return false
}
