package jevlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/systemone"
)

func TestLocalEvaluatorAskFallsBackWhenEngineNotReady(t *testing.T) {
	dir := openJevFixtureDir(t)

	eval, err := New(Options{ModelDir: dir, Model: "open-jev-test", DType: "q4"})
	if err != nil {
		t.Fatal(err)
	}
	if !eval.Configured() {
		t.Fatal("Configured should be true so Decider attempts Ask then falls back")
	}
	if eval.EngineName() != "unimplemented" {
		t.Fatalf("EngineName = %q", eval.EngineName())
	}

	_, _, err = eval.Ask(context.Background(), "state", map[string]systemone.Question{
		"q": systemone.Choice("which?", map[string]string{"a": "A", "b": "B"}),
	})
	if !errors.Is(err, ErrEngineNotReady) {
		t.Fatalf("Ask err = %v, want ErrEngineNotReady", err)
	}

	decider := systemone.NewDecider(eval)
	got, conf := decider.Choose(context.Background(), "state", "which?",
		map[string]string{"a": "A", "b": "B"}, 0.6, "a")
	if got != "a" || conf != 0 {
		t.Fatalf("Choose = %q/%v, want fallback a/0", got, conf)
	}
}

func TestLocalEvaluatorWithFakeEngineBuildsAndDecodes(t *testing.T) {
	dir := openJevFixtureDir(t)

	eval, err := New(Options{
		ModelDir: dir,
		Model:    "open-jev-test",
		DType:    "q4",
		Engine:   FakeEngine{Logits: []float32{0.1, 4.0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	answers, _, err := eval.Ask(context.Background(), "I was charged twice for the same order.", map[string]systemone.Question{
		"q": systemone.Choice("Which product area is the message about?", map[string]string{
			"fees":   "fees & charges",
			"refund": "refund & dispute",
		}),
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if answers["q"].Choice != "refund" {
		t.Fatalf("choice = %+v, want refund", answers["q"])
	}
}

func TestBuildOpenJevTensorsLayout(t *testing.T) {
	dir := openJevFixtureDir(t)
	tok, err := loadOpenJevTokenizer(dir)
	if err != nil {
		t.Fatal(err)
	}
	arts, err := ResolveArtifacts(dir, "q4", "t")
	if err != nil {
		t.Fatal(err)
	}
	plans := []questionPlan{
		{
			ID:           "area",
			Type:         systemone.TypeChoice,
			Instructions: "Which product area is the message about?",
			Options:      []string{"fees", "refund"},
			OptionTexts:  []string{"fees & charges", "refund & dispute"},
		},
		{
			ID:           "ask_refund",
			Type:         systemone.TypeNoul,
			Instructions: "The customer is asking for a refund.",
			Options:      []string{"no", "yes"},
			OptionTexts:  []string{"no", "yes"},
		},
	}
	bundle, kept, err := buildOpenJevTensors("I was charged twice for the same order.", plans, arts, tok)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 {
		t.Fatalf("kept = %d", len(kept))
	}
	if len(bundle.InputIDs) == 0 || len(bundle.InputIDs) != len(bundle.Seg) || len(bundle.InputIDs) != len(bundle.AttentionMask) {
		t.Fatalf("seq lens ids=%d seg=%d mask=%d", len(bundle.InputIDs), len(bundle.Seg), len(bundle.AttentionMask))
	}
	if bundle.InputIDs[0] != int64(tok.clsID) || bundle.InputIDs[1] != int64(tok.stateID) {
		t.Fatalf("prefix = %v %v, want CLS/STATE", bundle.InputIDs[0], bundle.InputIDs[1])
	}
	if bundle.InputIDs[len(bundle.InputIDs)-1] != int64(tok.sepID) {
		t.Fatalf("suffix = %v, want SEP", bundle.InputIDs[len(bundle.InputIDs)-1])
	}
	if len(bundle.PairOpt) != 4 || len(bundle.PairQ) != 4 {
		t.Fatalf("pairs = %d/%d", len(bundle.PairOpt), len(bundle.PairQ))
	}
	for i, id := range bundle.PairOpt {
		if id != int64(i) {
			t.Fatalf("PairOpt[%d]=%d", i, id)
		}
	}
	// Question slots are totalPairs + qi = 4 + qi.
	if bundle.PairQ[0] != 4 || bundle.PairQ[1] != 4 || bundle.PairQ[2] != 5 || bundle.PairQ[3] != 5 {
		t.Fatalf("PairQ = %v", bundle.PairQ)
	}
	foundQ, foundOpt := false, false
	for _, id := range bundle.InputIDs {
		if id == int64(tok.qID) {
			foundQ = true
		}
		if id == int64(tok.optID) {
			foundOpt = true
		}
	}
	if !foundQ || !foundOpt {
		t.Fatalf("missing markers q=%v opt=%v", foundQ, foundOpt)
	}
}

func openJevFixtureDir(t *testing.T) string {
	t.Helper()
	src := os.ExpandEnv(`C:\Users\solosw\.solcode\models\open-jev-deberta-v3-large`)
	if _, err := os.Stat(filepath.Join(src, "spm.model")); err != nil {
		t.Skip("open-jev model missing:", err)
	}
	dir := t.TempDir()
	// Copy only what New/ResolveArtifacts need; reuse real SPM so encode works.
	mustCopyFile(t, filepath.Join(src, "spm.model"), filepath.Join(dir, "spm.model"))
	mustCopyFile(t, filepath.Join(src, "open_jev_config.json"), filepath.Join(dir, "open_jev_config.json"))
	mustWrite(t, filepath.Join(dir, "tokenizer.json"), `{}`)
	onnxDir := filepath.Join(dir, "onnx")
	if err := os.MkdirAll(onnxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(onnxDir, "model_q4.onnx"), "onnx")
	return dir
}

func mustCopyFile(t *testing.T, src, dst string) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
