package changegraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// BuildContext returns a compact, deterministic project-change context. It
// favors the active session and recent events when no prompt relevance is
// available. Prefer BuildRelevantContext when the current user prompt is known.
func (s *Store) BuildContext(ctx context.Context, sessionID string, maxCharacters int) (string, error) {
	return s.BuildRelevantContext(ctx, sessionID, "", maxCharacters)
}

// BuildRelevantContext builds project-change context ranked by the current
// request. Matches against paths, descriptions, language, and affected symbols
// rank first; current-session and recency then break ties. This preserves
// continuity without burying a request-relevant cross-session change.
func (s *Store) BuildRelevantContext(ctx context.Context, sessionID, prompt string, maxCharacters int) (string, error) {
	// Prefer the active session's trace, then include recent project-wide
	// changes so useful work from other sessions remains visible.
	events, err := s.Recent(ctx, sessionID, 16)
	if err != nil {
		return "", err
	}
	projectEvents, err := s.Recent(ctx, "", 24)
	if err != nil {
		return "", err
	}
	seenEventIDs := make(map[int64]struct{}, len(events))
	for _, event := range events {
		seenEventIDs[event.ID] = struct{}{}
	}
	for _, event := range projectEvents {
		if _, seen := seenEventIDs[event.ID]; seen {
			continue
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return "", nil
	}
	rankEvents(events, sessionID, prompt)

	var b strings.Builder
	appendWithinBudget := func(text string) bool {
		if maxCharacters > 0 && len([]rune(b.String()))+len([]rune(text)) > maxCharacters {
			remaining := maxCharacters - len([]rune(b.String()))
			if remaining > 0 {
				runes := []rune(text)
				if remaining > len(runes) {
					remaining = len(runes)
				}
				b.WriteString(string(runes[:remaining]))
			}
			return false
		}
		b.WriteString(text)
		return true
	}
	if !appendWithinBudget("## Recent tracked changes\n") {
		return strings.TrimSpace(b.String()), nil
	}
	seen := map[string]bool{}
	for _, event := range events {
		key := event.Path + "\x00" + event.Description
		if seen[key] {
			continue
		}
		seen[key] = true
		when := event.OccurredAt.Local().Format(time.DateTime)
		var details []string
		if event.SessionID != "" && event.SessionID != sessionID {
			details = append(details, "session "+event.SessionID)
		}
		if event.ToolName != "" {
			details = append(details, event.ToolName)
		}
		if event.Language != "" {
			details = append(details, event.Language)
		}
		if event.ChangedLines != "" {
			details = append(details, "lines "+event.ChangedLines)
		}
		var entry strings.Builder
		entry.WriteString(fmt.Sprintf("- %s · %s — %s", when, event.Path, event.Description))
		if len(details) > 0 {
			entry.WriteString(" (" + strings.Join(details, ", ") + ")")
		}
		entry.WriteString("\n")
		if len(event.Symbols) > 0 {
			labels := make([]string, 0, len(event.Symbols))
			for _, symbol := range event.Symbols {
				name := symbol.FullName
				if name == "" {
					name = symbol.Name
				}
				if name != "" {
					labels = append(labels, string(normalizeSymbolChangeKind(symbol.ChangeKind))+" "+symbol.Kind+" "+name)
				}
			}
			if len(labels) > 0 {
				entry.WriteString("  - symbols: " + strings.Join(labels, ", ") + "\n")
			}
		}
		if !appendWithinBudget(entry.String()) {
			break
		}
	}
	return strings.TrimSpace(b.String()), nil
}

type rankedEvent struct {
	event     Event
	relevance int
	current   bool
}

func rankEvents(events []Event, sessionID, prompt string) {
	terms := queryTerms(prompt)
	ranked := make([]rankedEvent, len(events))
	for i, event := range events {
		ranked[i] = rankedEvent{
			event:     event,
			relevance: eventRelevance(event, terms),
			current:   sessionID != "" && event.SessionID == sessionID,
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].relevance != ranked[j].relevance {
			return ranked[i].relevance > ranked[j].relevance
		}
		if ranked[i].current != ranked[j].current {
			return ranked[i].current
		}
		return ranked[i].event.OccurredAt.After(ranked[j].event.OccurredAt)
	})
	for i := range ranked {
		events[i] = ranked[i].event
	}
}

func eventRelevance(event Event, terms map[string]struct{}) int {
	if len(terms) == 0 {
		return 0
	}
	fields := []string{event.Path, event.Description, event.Language}
	for _, symbol := range event.Symbols {
		fields = append(fields, symbol.Name, symbol.FullName, symbol.Kind)
	}
	score := 0
	for _, field := range fields {
		for token := range queryTerms(field) {
			if _, matched := terms[token]; matched {
				score++
			}
		}
	}
	return score
}

func queryTerms(text string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(token)) >= 2 {
			terms[token] = struct{}{}
		}
	}
	return terms
}
