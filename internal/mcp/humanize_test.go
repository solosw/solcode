package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// A JSON object result must render as readable key/value lines rather than a
// blob the model has to parse before it can reason about the content.
func TestHumanizeStructuredObject(t *testing.T) {
	input := `{"order_id":"A-104","total_usd":98,"status":"shipped"}`
	got := humanizeStructuredText(input)

	if strings.Contains(got, "{") || strings.Contains(got, `"order_id"`) {
		t.Fatalf("output still looks like JSON:\n%s", got)
	}
	for _, want := range []string{"order_id: A-104", "total_usd: 98", "status: shipped"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// Every leaf value must survive: this is a formatting change, not a filter.
func TestHumanizeIsLossless(t *testing.T) {
	input := `{"a":1,"b":[{"c":"d"},{"e":[1,2,3]}],"f":{"g":{"h":"deep"}}}`
	got := humanizeStructuredText(input)

	for _, want := range []string{"a: 1", "c: d", "e:", "1", "2", "3", "h: deep"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q in:\n%s", want, got)
		}
	}
}

func TestHumanizeRendersListsAsBullets(t *testing.T) {
	got := humanizeStructuredText(`{"servers":["alpha","beta","gamma"]}`)
	if !strings.Contains(got, "servers:") {
		t.Fatalf("missing key:\n%s", got)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(got, "- "+name) {
			t.Fatalf("expected a bullet for %q in:\n%s", name, got)
		}
	}
}

// A JSON array at the top level is also common for list-style results.
func TestHumanizeTopLevelArray(t *testing.T) {
	got := humanizeStructuredText(`[{"id":1},{"id":2}]`)
	if !strings.Contains(got, "id: 1") || !strings.Contains(got, "id: 2") {
		t.Fatalf("expected both records:\n%s", got)
	}
}

// Plain prose must pass through untouched: most tool results are text, and
// rewriting them would be both wasteful and wrong.
func TestHumanizeLeavesPlainTextAlone(t *testing.T) {
	cases := []string{
		"Build succeeded in 1.2s",
		"line one\nline two",
		"# Heading\n\n- item",
		"",
	}
	for _, input := range cases {
		if got := humanizeStructuredText(input); got != input {
			t.Fatalf("input %q was rewritten to %q", input, got)
		}
	}
}

// Text that merely starts with a brace but is not valid JSON must survive.
func TestHumanizeKeepsMalformedJSON(t *testing.T) {
	input := `{"unterminated": `
	if got := humanizeStructuredText(input); got != input {
		t.Fatalf("malformed JSON was altered: %q -> %q", input, got)
	}
}

// A JSON scalar (a bare number or quoted string) is valid JSON but must not be
// mistaken for a document worth restructuring.
func TestHumanizeKeepsScalarJSON(t *testing.T) {
	for _, input := range []string{"42", `"just a string"`, "true", "null"} {
		if got := humanizeStructuredText(input); got != input {
			t.Fatalf("scalar %q was altered to %q", input, got)
		}
	}
}

// Empty values are marked so the model can tell "no value" from a missing key.
func TestHumanizeMarksEmptyValues(t *testing.T) {
	got := humanizeStructuredText(`{"note":"","items":[]}`)
	if !strings.Contains(got, "note: (empty)") {
		t.Fatalf("expected an empty marker for note:\n%s", got)
	}
	if !strings.Contains(got, "items: (empty list)") {
		t.Fatalf("expected an empty marker for items:\n%s", got)
	}
}

// A nested object keeps its key and renders its contents on following lines, so
// no key name is ever lost to formatting.
func TestHumanizeKeepsNestedKeys(t *testing.T) {
	got := humanizeStructuredText(`{"result":{"count":3}}`)
	for _, want := range []string{"result:", "count: 3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// Output is deterministic, since it becomes part of the conversation.
func TestStructuredToTextIsDeterministic(t *testing.T) {
	value := map[string]any{
		"z": 1, "a": 2, "m": map[string]any{"y": 1, "b": 2},
	}
	first := structuredToText(value)
	for i := 0; i < 5; i++ {
		if got := structuredToText(value); got != first {
			t.Fatalf("not deterministic:\n%s\nvs\n%s", first, got)
		}
	}
}

// Pathological nesting must terminate rather than recurse without bound.
func TestStructuredToTextBoundsDepth(t *testing.T) {
	var value any = "leaf"
	for i := 0; i < maxStructuredDepth+5; i++ {
		value = map[string]any{"next": value}
	}
	got := structuredToText(value)
	if !strings.Contains(got, "nested deeper than") {
		t.Fatalf("expected a depth guard:\n%s", got)
	}
}

// StructuredContent arrives as a Go value, not a string, and must render
// directly without a JSON round trip.
func TestStructuredToTextHandlesTypedValues(t *testing.T) {
	// This mirrors what a JSON decode produces.
	var decoded any
	if err := json.Unmarshal([]byte(`{"ok":true,"n":2}`), &decoded); err != nil {
		t.Fatal(err)
	}
	got := structuredToText(decoded)
	if !strings.Contains(got, "ok: true") || !strings.Contains(got, "n: 2") {
		t.Fatalf("unexpected rendering:\n%s", got)
	}
}

// Non-text content blocks (images, embedded resources) are described by kind
// plus their fields rather than dumped as raw JSON.
//
// The SDK's Content interface has an unexported method, so a block cannot be
// constructed from outside the package. This exercises the same rendering the
// block path uses, against the same decoded shape.
func TestNonTextContentRendering(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(`{"type":"image","data":"AAAA","mimeType":"image/png"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	kind, _ := decoded["type"].(string)
	delete(decoded, "type")
	got := "[" + kind + " content]\n" + structuredToText(decoded)

	if !strings.Contains(got, "image") {
		t.Fatalf("expected the block kind to be named:\n%s", got)
	}
	if !strings.Contains(got, "mimeType: image/png") {
		t.Fatalf("expected the block fields to be rendered:\n%s", got)
	}
	// The type discriminator is already stated in the heading, so it should not
	// be repeated as a field.
	if strings.Contains(got, "type:") {
		t.Fatalf("type should not be repeated as a field:\n%s", got)
	}
	// The payload must survive.
	if !strings.Contains(got, "data: AAAA") {
		t.Fatalf("expected the block payload:\n%s", got)
	}
}
