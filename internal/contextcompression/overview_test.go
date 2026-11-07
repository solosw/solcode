package contextcompression

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	contextmessage "github.com/solosw/solcode/internal/message"
	"testing"
)

func TestOverviewRangesCanBeCompressedDirectly(t *testing.T) {
	s := New()
	if err := s.Sync(history()); err != nil {
		t.Fatal(err)
	}
	overview := s.Overview(100_000, 0)
	if overview.TotalCompressibleRanges != 1 || overview.TotalProtectedRanges != 1 || overview.EstimatedSavings <= 0 {
		t.Fatalf("%+v", overview)
	}
	r := overview.CompressibleRanges[0]
	if r.Start != "m1" || r.End != "m6" {
		t.Fatalf("%+v", r)
	}
	if _, err := s.Compress(Request{Start: r.Start, End: r.End, Summary: "Completed old investigation.", Revision: &overview.Revision}); err != nil {
		t.Fatal("advertised range rejected", err)
	}
	if got := s.Overview(100_000, 0); got.Revision != 1 || len(got.Blocks) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestOverviewExpandsProtectionAcrossToolGroups(t *testing.T) {
	messages := history()
	overlay := sdk.NewUserMessage(sdk.NewTextBlock(contextmessage.DynamicContextText("mode", "current policy")))
	messages = append(messages[:4], append([]sdk.MessageParam{overlay}, messages[4:]...)...)
	s := New()
	if err := s.Sync(messages); err != nil {
		t.Fatal(err)
	}
	o := s.Overview(100_000, 0)
	for _, r := range o.CompressibleRanges {
		a, b, err := s.Range(r.Start, r.End)
		if err != nil {
			t.Fatal(err)
		}
		safe := boundaries(messages)
		if !safe[a] || !safe[b] {
			t.Fatalf("unsafe range %+v", r)
		}
		for i := a; i < b; i++ {
			if i >= 3 && i <= 5 {
				t.Fatalf("protected exchange advertised: %+v", r)
			}
		}
	}
	limited := s.Overview(100_000, 1)
	if len(limited.CompressibleRanges) > 1 || len(limited.ProtectedRanges) > 1 || limited.TotalProtectedRanges != o.TotalProtectedRanges || limited.CompressibleTokens != o.CompressibleTokens {
		t.Fatal("bounded overview lost totals")
	}
}
