package contextcompression

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func history() []sdk.MessageParam {
	return []sdk.MessageParam{
		sdk.NewUserMessage(sdk.NewTextBlock("unchanged prefix")),
		sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("completed investigation evidence ", 200))),
		sdk.NewUserMessage(sdk.NewTextBlock("old requirement")),
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("read", map[string]any{"path": "a.go"}, "View")),
		sdk.NewUserMessage(sdk.NewToolResultBlock("read", strings.Repeat("准确证据🧪 ", 500), false)),
		sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("completed old work ", 300))),
		sdk.NewUserMessage(sdk.NewTextBlock("CURRENT request must stay")),
		sdk.NewAssistantMessage(sdk.NewToolUseBlock("compress-call", map[string]any{}, "compress")),
	}
}
func TestFoldHierarchyRestoreAndStablePrefix(t *testing.T) {
	original := history()
	s := New()
	if err := s.Sync(original); err != nil {
		t.Fatal(err)
	}
	prefix, _ := json.Marshal(original[0])
	tail, _ := json.Marshal(original[6:])
	if _, err := s.Compress(Request{Start: "m2", End: "m5", Summary: "Read a.go; original tool evidence is archived."}); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Messages()
	b, _ := json.Marshal(first[0])
	if string(b) != string(prefix) {
		t.Fatal("prefix rewritten")
	}
	b, _ = json.Marshal(first[len(first)-2:])
	if string(b) != string(tail) {
		t.Fatal("recent messages rewritten")
	}
	firstBlock, _ := json.Marshal(first[1])
	if err := s.Sync(first); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Messages()
	b, _ = json.Marshal(again[1])
	if string(b) != string(firstBlock) {
		t.Fatal("summary changed on replay")
	}
	if _, err := s.Compress(Request{Start: "b1", End: "m6", Summary: "Completed investigation; preserve current request."}); err != nil {
		t.Fatal(err)
	}
	if s.Blocks[1].Level != 2 {
		t.Fatal("missing hierarchy")
	}
	// Persist/reload must retain originals and block references exactly.
	raw, _ := json.Marshal(s)
	var loaded State
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Restore("b2", true, 1); err == nil {
		t.Fatal("over-budget restore accepted")
	}
	if err := loaded.Restore("b2", true, 0); err != nil {
		t.Fatal(err)
	}
	restored, _ := loaded.Messages()
	beforeJSON, _ := json.Marshal(original)
	afterJSON, _ := json.Marshal(restored)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("deep restore changed serialized messages:\n%s\n%s", beforeJSON, afterJSON)
	}
}
func TestRejectsInvalidRangesWithoutMutation(t *testing.T) {
	s := New()
	_ = s.Sync(history())
	rev := 1
	for _, req := range []Request{
		{Start: "m4", End: "m4", Summary: "split tool call"},
		{Start: "m5", End: "m6", Summary: "split tool result"},
		{Start: "m1", End: "m7", Summary: "current request removed"},
		{Start: "m2", End: "m5", Summary: "stale", Revision: &rev},
		{Start: "missing", End: "m5", Summary: "unknown"},
		{Start: "m2", End: "m5"},
		{Start: "m2", End: "m5", Summary: strings.Repeat("not reduced ", 5000)},
	} {
		before, _ := json.Marshal(s)
		if _, err := s.Compress(req); err == nil {
			t.Fatalf("accepted invalid request %+v", req)
		}
		after, _ := json.Marshal(s)
		if string(before) != string(after) {
			t.Fatal("rejected fold changed state")
		}
	}
}
func TestForkCarriesOnlyReachableEvidence(t *testing.T) {
	s := New()
	_ = s.Sync(history())
	_, err := s.Compress(Request{Start: "m2", End: "m5", Summary: "old evidence"})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := s.Messages()
	child := s.Fork(view[1:])
	if _, err := child.message("m1"); err == nil {
		t.Fatal("excluded parent context leaked")
	}
	if _, err := child.Read("b1", true, 0, 20); err != nil {
		t.Fatal("inherited fold not readable:", err)
	}
	if err := child.Restore("b1", true, 0); err != nil {
		t.Fatal(err)
	}
	parent, _ := s.Messages()
	if len(parent) != len(view) {
		t.Fatal("child changed parent")
	}
	sibling := New()
	if _, err := sibling.Read("b1", true, 0, 5); err == nil {
		t.Fatal("cross-session block readable")
	}
}
func TestLargeSourceReadIsExactAcrossPages(t *testing.T) {
	s := New()
	m := sdk.NewUserMessage(sdk.NewTextBlock(strings.Repeat("证据🧪\n", 5000)))
	_ = s.Sync([]sdk.MessageParam{m})
	var text strings.Builder
	for offset := 0; ; {
		page, err := s.Read("m1", false, offset, 8000)
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(page["text"].(string))
		offset = page["next_offset"].(int)
		if offset == page["total_runes"].(int) {
			break
		}
	}
	if text.String() != Text(m) {
		t.Fatal("paged read lost or changed evidence")
	}
}
func TestSyncHandlesDuplicateMessagesAndRemovedOverlay(t *testing.T) {
	m := sdk.NewUserMessage(sdk.NewTextBlock("same"))
	s := New()
	_ = s.Sync([]sdk.MessageParam{m, m, m})
	refs := append([]string(nil), s.Active...)
	_ = s.Sync([]sdk.MessageParam{m, m, m})
	if !reflect.DeepEqual(refs, s.Active) || len(s.Sources) != 3 {
		t.Fatal("duplicate message refs unstable")
	}
}
