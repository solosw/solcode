package sessionmemory

import (
	"fmt"
	"strings"
)

// TodoStatus values recorded with a turn snapshot.
const (
	TodoPending    = "pending"
	TodoInProgress = "in_progress"
	TodoCompleted  = "completed"
)

// TodoJudgment is one todolist item captured for a turn, optionally annotated
// by Jev with whether it is still valid for the task and whether it is done.
type TodoJudgment struct {
	ID      string
	Content string
	Status  string
	// Valid is true when Jev judged the item relevant to the task. When Jev is
	// off, Valid defaults to true so the snapshot stays useful.
	Valid bool
	// Done is true when Jev judged the item completed for the task, or when the
	// model already marked the item completed.
	Done bool
}

func formatTodos(todos []TodoJudgment) string {
	if len(todos) == 0 {
		return ""
	}
	parts := make([]string, 0, len(todos))
	for _, todo := range todos {
		id := strings.TrimSpace(todo.ID)
		content := collapseSpaces(strings.TrimSpace(todo.Content))
		if id == "" && content == "" {
			continue
		}
		status := strings.TrimSpace(todo.Status)
		if status == "" {
			status = TodoPending
		}
		valid := "valid"
		if !todo.Valid {
			valid = "invalid"
		}
		done := "open"
		if todo.Done {
			done = "done"
		}
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%s|%s",
			escapeTodoField(id),
			escapeTodoField(content),
			escapeTodoField(status),
			valid,
			done,
		))
	}
	return strings.Join(parts, "; ")
}

func parseTodos(raw string) []TodoJudgment {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	chunks := splitEscaped(raw, ';')
	out := make([]TodoJudgment, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		fields := splitEscaped(chunk, '|')
		if len(fields) < 3 {
			continue
		}
		todo := TodoJudgment{
			ID:      unescapeTodoField(fields[0]),
			Content: unescapeTodoField(fields[1]),
			Status:  unescapeTodoField(fields[2]),
			Valid:   true,
		}
		if len(fields) > 3 && strings.EqualFold(strings.TrimSpace(fields[3]), "invalid") {
			todo.Valid = false
		}
		if len(fields) > 4 && strings.EqualFold(strings.TrimSpace(fields[4]), "done") {
			todo.Done = true
		}
		if todo.Status == TodoCompleted {
			todo.Done = true
		}
		out = append(out, todo)
	}
	return out
}

func splitEscaped(value string, sep rune) []string {
	var parts []string
	var b strings.Builder
	escaped := false
	for _, r := range value {
		if escaped {
			b.WriteRune('\\')
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == sep {
			parts = append(parts, b.String())
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		b.WriteByte('\\')
	}
	parts = append(parts, b.String())
	return parts
}

// MergeTodos keeps the newest turn's item for each id (or content when id is
// empty), preserving turn order so later snapshots win. Invalid items are kept
// in the merged view but marked invalid so callers can filter.
func MergeTodos(entries []Entry) []TodoJudgment {
	type keyed struct {
		todo TodoJudgment
		turn int
	}
	best := map[string]keyed{}
	order := make([]string, 0)
	for _, entry := range entries {
		for _, todo := range entry.Todos {
			key := strings.TrimSpace(todo.ID)
			if key == "" {
				key = "content:" + strings.ToLower(strings.TrimSpace(todo.Content))
			}
			if key == "" || key == "content:" {
				continue
			}
			prev, ok := best[key]
			if !ok {
				best[key] = keyed{todo: todo, turn: entry.Turn}
				order = append(order, key)
				continue
			}
			if entry.Turn >= prev.turn {
				best[key] = keyed{todo: todo, turn: entry.Turn}
			}
		}
	}
	out := make([]TodoJudgment, 0, len(order))
	for _, key := range order {
		out = append(out, best[key].todo)
	}
	return out
}

func escapeTodoField(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `|`, `\|`)
	value = strings.ReplaceAll(value, `;`, `\;`)
	return value
}

func unescapeTodoField(value string) string {
	var b strings.Builder
	escaped := false
	for _, r := range value {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		b.WriteByte('\\')
	}
	return b.String()
}

func collapseSpaces(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
