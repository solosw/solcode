package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// structuredToText renders a structured tool payload as readable text instead of
// a raw JSON blob.
//
// MCP servers commonly return objects, arrays, or nested records. Dumping
// `{"a":1,"b":[{"c":"d"}]}` at the model forces it to parse JSON before it can
// reason about the result, spending attention on syntax rather than content, and
// a deeply nested or large payload is easy to misread. This renders the same
// data as indented key/value lines that read like a report.
//
// It is deliberately lossless: every leaf value appears. Only formatting
// changes, so nothing the model needs can be dropped.
func structuredToText(value any) string {
	var b strings.Builder
	writeStructured(&b, value, 0)
	return strings.TrimRight(b.String(), "\n")
}

// maxStructuredDepth bounds recursion so a self-referential or pathologically
// nested payload cannot produce unbounded output.
const maxStructuredDepth = 12

func writeStructured(b *strings.Builder, value any, depth int) {
	if depth > maxStructuredDepth {
		b.WriteString("… (nested deeper than " + fmt.Sprint(maxStructuredDepth) + " levels)\n")
		return
	}
	switch typed := value.(type) {
	case nil:
		b.WriteString("(none)\n")
	case string:
		b.WriteString(scalarText(typed))
		b.WriteString("\n")
	case bool:
		b.WriteString(fmt.Sprintf("%t\n", typed))
	case float64, int, int64, json.Number:
		b.WriteString(fmt.Sprintf("%v\n", typed))
	case []any:
		writeStructuredList(b, typed, depth)
	case map[string]any:
		writeStructuredObject(b, typed, depth)
	default:
		// Unknown shapes (custom types, json.RawMessage that failed to decode)
		// fall back to their JSON form rather than being dropped.
		if raw, err := json.Marshal(typed); err == nil {
			b.WriteString(string(raw))
			b.WriteString("\n")
			return
		}
		b.WriteString(fmt.Sprintf("%v\n", typed))
	}
}

func writeStructuredList(b *strings.Builder, items []any, depth int) {
	if len(items) == 0 {
		b.WriteString("(empty list)\n")
		return
	}
	indent := strings.Repeat("  ", depth)
	for i, item := range items {
		b.WriteString(fmt.Sprintf("%s- ", indent))
		// A scalar item stays on the bullet line; anything structured moves to
		// the next line so the bullet list stays readable.
		if isScalar(item) {
			writeStructured(b, item, depth+1)
			continue
		}
		b.WriteString("\n")
		writeStructured(b, item, depth+2)
		_ = i
	}
}

func writeStructuredObject(b *strings.Builder, object map[string]any, depth int) {
	if len(object) == 0 {
		b.WriteString("(empty)\n")
		return
	}
	indent := strings.Repeat("  ", depth)
	for _, key := range sortedKeys(object) {
		value := object[key]
		if isScalar(value) {
			b.WriteString(fmt.Sprintf("%s%s: ", indent, key))
			writeStructured(b, value, depth+1)
			continue
		}
		// A nested container gets its own block on the following lines; a scalar
		// stays on the key line.
		b.WriteString(fmt.Sprintf("%s%s:\n", indent, key))
		writeStructured(b, value, depth+1)
	}
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isScalar(value any) bool {
	switch typed := value.(type) {
	case nil, string, bool, float64, int, int64, json.Number:
		return true
	case []any:
		// An empty container renders as a single marker on the key line rather
		// than an orphaned block below it.
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

// scalarText keeps a scalar string intact, including embedded newlines, but
// marks an empty string so the model can tell "no value" from a missing key.
func scalarText(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(empty)"
	}
	return value
}

// decodeStructured attempts to read a JSON document into a generic value so it
// can be rendered. It returns false when the text is not JSON, which is the
// common case for a plain-text tool result.
func decodeStructured(text string) (any, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	// Only attempt a decode when the text plausibly is JSON: a leading brace or
	// bracket. This avoids turning ordinary prose into a decode attempt.
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

// humanizeStructuredText converts JSON-looking tool output into readable text,
// leaving anything that is not JSON untouched.
func humanizeStructuredText(text string) string {
	decoded, ok := decodeStructured(text)
	if !ok {
		return text
	}
	rendered := structuredToText(decoded)
	if strings.TrimSpace(rendered) == "" {
		return text
	}
	return rendered
}
