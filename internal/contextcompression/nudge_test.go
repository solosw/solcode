package contextcompression

import (
	"encoding/json"
	"testing"
)

func TestNudgePressureAndSuppression(t *testing.T) {
	useful := Overview{TotalCompressibleRanges: 1, EstimatedSavings: 30_000}
	for _, tc := range []struct {
		name     string
		tokens   int
		overview Overview
		want     string
	}{
		{"below minimum", 44_999, useful, ""},
		{"middle requires growth", 45_000, useful, ""},
		{"below high", 74_999, useful, ""},
		{"high", 75_000, useful, "pressure"},
		{"before emergency low benefit", 94_999, Overview{TotalCompressibleRanges: 1, EstimatedSavings: 1}, ""},
		{"emergency low benefit", 95_000, Overview{TotalCompressibleRanges: 1, EstimatedSavings: 1}, "emergency"},
		{"no eligible history", 99_000, Overview{}, ""},
		{"no possible savings", 99_000, Overview{TotalCompressibleRanges: 1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n NudgeState
			if got := n.Decide(NudgeConfig{}, tc.tokens, 100_000, tc.overview); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	var n NudgeState
	if got := n.Decide(NudgeConfig{}, 75_000, 100_000, useful); got != "pressure" {
		t.Fatal(got)
	}
	// Growth alone cannot bypass the frequency gate.
	if got := n.Decide(NudgeConfig{}, 94_999, 100_000, useful); got != "" {
		t.Fatal(got)
	}
	for range 4 {
		if got := n.Decide(NudgeConfig{}, 75_000, 100_000, useful); got != "" {
			t.Fatal("unchanged context reminded", got)
		}
	}
	if got := n.Decide(NudgeConfig{}, 94_999, 100_000, useful); got != "" {
		t.Fatal("insufficient growth", got)
	}
	// Emergency escalation bypasses a recent pressure reminder.
	if got := n.Decide(NudgeConfig{}, 95_000, 100_000, useful); got != "emergency" {
		t.Fatal(got)
	}
	if got := n.Decide(NudgeConfig{}, 95_000, 100_000, useful); got != "" {
		t.Fatal("repeated emergency", got)
	}
}

func TestNudgeGrowthPersistenceAndCompression(t *testing.T) {
	useful := Overview{TotalCompressibleRanges: 1, EstimatedSavings: 60_000}
	var n NudgeState
	n.Decide(NudgeConfig{}, 80_000, 300_000, useful)
	for range 14 {
		if got := n.Decide(NudgeConfig{}, 135_000, 300_000, useful); got != "" {
			t.Fatal("iteration suppression", got)
		}
	}
	if got := n.Decide(NudgeConfig{}, 135_000, 300_000, useful); got != "growth" {
		t.Fatal(got)
	}
	s := New()
	s.Nudge = n
	raw, _ := json.Marshal(s)
	var loaded State
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if got := loaded.Nudge.Decide(NudgeConfig{}, 225_000, 300_000, useful); got != "" {
		t.Fatal("lost cooldown", got)
	}
	for range 4 {
		loaded.Nudge.Decide(NudgeConfig{}, 135_000, 300_000, useful)
	}
	if got := loaded.Nudge.Decide(NudgeConfig{}, 225_000, 300_000, useful); got != "pressure" {
		t.Fatal(got)
	}
	loaded.Nudge.Compressed(100_000)
	if got := loaded.Nudge.Decide(NudgeConfig{}, 225_000, 300_000, useful); got != "" {
		t.Fatal("compression should reset cooldown", got)
	}
	child := loaded.Fork(nil)
	if child.Nudge.Initialized || loaded.Nudge.Requests == 0 {
		t.Fatal("fork inherited or mutated scheduling")
	}
}

func TestNudgeResetsForChangedBudget(t *testing.T) {
	var n NudgeState
	useful := Overview{TotalCompressibleRanges: 1, EstimatedSavings: 30000}
	if got := n.Decide(NudgeConfig{}, 150000, 200000, useful); got != "pressure" {
		t.Fatal(got)
	}
	if got := n.Decide(NudgeConfig{}, 150000, 180000, useful); got != "pressure" {
		t.Fatal("new budget inherited old cooldown", got)
	}
}

func TestNudgeConfiguration(t *testing.T) {
	c := NudgeConfig{MinPressure: .2, MaxPressure: .4, EmergencyPressure: .6, Frequency: 2, IterationThreshold: 3, GrowthTokens: 100, MinGrowthTokens: 50, MinBenefitTokens: 10}
	var n NudgeState
	useful := Overview{TotalCompressibleRanges: 1, EstimatedSavings: 10}
	if got := n.Decide(c, 400, 1000, useful); got != "pressure" {
		t.Fatal(got)
	}
	if got := n.Decide(c, 500, 1000, useful); got != "" {
		t.Fatal(got)
	}
	if got := n.Decide(c, 500, 1000, useful); got != "pressure" {
		t.Fatal(got)
	}
	if got := n.Decide(c, 600, 1000, useful); got != "emergency" {
		t.Fatal(got)
	}
}
