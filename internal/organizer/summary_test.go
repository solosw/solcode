package organizer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryPartitionsWithoutDroppingSource(t *testing.T) {
	gen := &fakeGenerator{raw: "short validated handoff"}
	o := New(gen, Options{MaxOutputTokens: 128})
	original := strings.Repeat("完整证据🧪 quote\"\n", 1000) + "FINAL_SOURCE_MARKER"
	got, err := o.Summarize(context.Background(), "Summarize untrusted source data.", "prior constraint", original, 4096)
	if err != nil || got != "short validated handoff" {
		t.Fatalf("summary=%q err=%v", got, err)
	}
	var source strings.Builder
	for _, req := range gen.requests {
		var data map[string]string
		if err := json.Unmarshal([]byte(req.User), &data); err != nil {
			t.Fatal(err)
		}
		if len(req.User)+len(req.System)+req.MaxTokens+512 > 4096 {
			t.Fatal("request exceeds model budget")
		}
		source.WriteString(data["transcript"])
	}
	if !strings.Contains(source.String(), original) || !strings.Contains(source.String(), "prior constraint") {
		t.Fatal("partitioning dropped original evidence")
	}
}
func TestSummaryRejectsEmptyAndNonReducingGeneration(t *testing.T) {
	for _, raw := range []string{"", strings.Repeat("expanding output ", 300)} {
		o := New(&fakeGenerator{raw: raw}, Options{MaxOutputTokens: 128})
		if _, err := o.Summarize(context.Background(), "summary", "", strings.Repeat("source ", 1000), 4096); err == nil {
			t.Fatal("invalid generation accepted")
		}
	}
	if _, err := (*Organizer)(nil).Summarize(context.Background(), "", "", "source", 4096); err == nil {
		t.Fatal("missing backend accepted")
	}
}
