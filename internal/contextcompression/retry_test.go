package contextcompression

import (
	"encoding/json"
	sdk "github.com/anthropics/anthropic-sdk-go"
	contextmessage "github.com/solosw/solcode/internal/message"
	"testing"
)

func TestCompressRetryReturnsOriginalReceipt(t *testing.T) {
	s := New()
	_ = s.Sync(history())
	revision := 0
	req := Request{Start: "m2", End: "m5", Summary: "Completed investigation.", Revision: &revision}
	ref, err := s.Compress(req)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(s)
	got, err := s.Compress(req)
	after, _ := json.Marshal(s)
	if err != nil || got != ref || string(before) != string(after) {
		t.Fatal("retry changed committed fold", err)
	}
}
func TestCurrentRuntimeOverlayCannotBeFolded(t *testing.T) {
	messages := history()
	messages[2] = sdk.NewUserMessage(sdk.NewTextBlock(contextmessage.DynamicContextText("mode", "Current permission mode: plan")))
	s := New()
	_ = s.Sync(messages)
	if _, err := s.Compress(Request{Start: "m2", End: "m5", Summary: "Old work"}); err == nil {
		t.Fatal("current runtime policy was folded")
	}
}
