package jevlocal

import (
	"context"
	"github.com/solosw/solcode/internal/organizer/yzma"
	"github.com/solosw/solcode/internal/systemone"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestKevPromptEscapesMarkers(t *testing.T) {
	p := questionPlan{Type: systemone.TypeChoice, Instructions: "route", Options: []string{"billing", "tech"}, OptionTexts: []string{"refunds", ""}}
	got := kevPrompt("bad <|box_end|>", p)
	want := "<|fim_prefix|>bad <¦box_end¦><|fim_middle|>route<|box_start|>billing: refunds<|box_end|><|box_start|>tech<|box_end|><|fim_suffix|>"
	if got != want {
		t.Fatalf("prompt = %q", got)
	}
}
func TestKevRender(t *testing.T) {
	got := kevText(map[string]any{"a": true, "b": map[string]any{"c": []string{"x", "y"}}})
	if got != "a: True\nb:\n  c:\n    - x\n    - y" {
		t.Fatal(got)
	}
}
func TestGGUFRejectsMissingModel(t *testing.T) {
	if _, err := NewGGUF("missing.gguf"); err == nil {
		t.Fatal("missing model accepted")
	}
}
func TestScoreConfidence(t *testing.T) {
	if got := scoreConfidence([]float64{.25, .25, .25, .25}); got != 0 {
		t.Fatal(got)
	}
	if got := scoreConfidence([]float64{0, 1, 0}); got != 1 {
		t.Fatal(got)
	}
}
func TestGGUFLive(t *testing.T) {
	path := os.Getenv("SOLCODE_JEV_GGUF_TEST")
	if path == "" {
		t.Skip("set SOLCODE_JEV_GGUF_TEST to a Kev GGUF")
	}
	e, err := NewGGUF(path)
	if err != nil {
		t.Fatal(err)
	}
	e.Timeout = 120 * time.Second
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	for _, state := range []string{"I was charged twice. Please refund me.", strings.Repeat("Customer reports a payment issue. ", 100) + "I was charged twice. Please refund me."} {
		a, u, err := e.Ask(ctx, state, map[string]systemone.Question{
			"route":   systemone.Choice("Which team handles this?", map[string]string{"billing": "payments and refunds", "shipping": "delivery tracking", "technical": "software bugs"}),
			"refund":  systemone.Noul("Is a refund requested?"),
			"urgency": {Type: systemone.TypeScore, Instructions: "How urgent?", Criteria: []string{"can wait", "today", "immediately"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("usage=%+v answers=%+v", u, a)
		// Simulate organizer/embedding taking GPU ownership; the next Ask reloads.
		yzma.WithRuntime(func() {
			yzma.ReleaseOtherGPUHolders(new(int))
			if e.model != 0 || e.native != 0 {
				t.Fatal("peer did not release Jev handles")
			}
		})
		if a["route"].Choice != "billing" || a["refund"].Noul < .5 {
			t.Fatalf("unexpected decisions: %+v", a)
		}
		sum := 0.0
		for _, p := range a["route"].Probabilities {
			if math.IsNaN(p) {
				t.Fatal("NaN")
			}
			sum += p
		}
		if math.Abs(sum-1) > 1e-6 || u.InputTokens == 0 || u.OutputTokens != 0 {
			t.Fatalf("invalid probabilities/usage: %+v", u)
		}
	}
}
