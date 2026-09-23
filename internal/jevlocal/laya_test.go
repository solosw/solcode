package jevlocal

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/systemone"
)

func TestLayaFamilyBuildAndDecodeFake(t *testing.T) {
	src := os.ExpandEnv(`C:\Users\solosw\.solcode\models\laya-onnx`)
	if _, err := os.Stat(filepath.Join(src, "tokenizer.json")); err != nil {
		t.Skip("laya model missing:", err)
	}
	dir := t.TempDir()
	mustCopyFile(t, filepath.Join(src, "tokenizer.json"), filepath.Join(dir, "tokenizer.json"))
	mustCopyFile(t, filepath.Join(src, "rl_agent_config.json"), filepath.Join(dir, "rl_agent_config.json"))
	mustWrite(t, filepath.Join(dir, "model.onnx"), "onnx")

	arts, err := ResolveArtifacts(dir, "fp32", "laya-test")
	if err != nil {
		t.Fatal(err)
	}
	fam, err := newLayaFamily(arts)
	if err != nil {
		t.Fatalf("newLayaFamily: %v", err)
	}
	questions := map[string]systemone.Question{
		"area": systemone.Choice("Which product area?", map[string]string{
			"fees":   "fees & charges",
			"refund": "refund & dispute",
		}),
		"ask_refund": systemone.Noul("The customer is asking for a refund."),
	}
	tensors, plans, err := fam.Build(context.Background(), "I was charged twice.", questions)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("plans = %d", len(plans))
	}
	for _, name := range []string{"input_ids", "attention_mask", "marker_pos", "qtype"} {
		if _, ok := tensors.Int64[name]; !ok {
			t.Fatalf("missing int64 %s", name)
		}
	}
	if _, ok := tensors.Bool["marker_mask"]; !ok {
		t.Fatal("missing marker_mask")
	}
	if got := tensors.Int64["qtype"].Shape; len(got) != 1 || got[0] != 2 {
		t.Fatalf("qtype shape = %v", got)
	}
	kmax := int(tensors.Int64["marker_pos"].Shape[1])
	// Strong refund / yes across padded marker slots.
	logits := make([]float32, 2*kmax)
	logits[0], logits[1] = 0, 4            // choice fees/refund
	logits[kmax+0], logits[kmax+1] = -2, 3 // noul no/yes
	outs := NamedOutputs{Float32: map[string]Float32Tensor{
		"logits": {Shape: []int64{2, int64(kmax)}, Data: logits},
	}}
	answers, err := fam.Decode(plans, outs)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if answers["area"].Choice != "refund" {
		t.Fatalf("area = %+v", answers["area"])
	}
	if answers["ask_refund"].Noul < 0.8 {
		t.Fatalf("noul = %v", answers["ask_refund"].Noul)
	}
}

func TestLocalEvaluatorAskLayaORTEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		os.Setenv("CGO_ENABLED", "1")
	}
	modelDir := os.ExpandEnv(`C:\Users\solosw\.solcode\models\laya-onnx`)
	dll := DefaultORTLibrary()
	if _, err := os.Stat(dll); err != nil {
		t.Skip("ort dll missing:", err)
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.onnx")); err != nil {
		t.Skip("laya onnx missing:", err)
	}
	if _, err := os.Stat(filepath.Join(modelDir, "tokenizer.json")); err != nil {
		t.Skip("laya tokenizer missing:", err)
	}

	start := time.Now()
	eval, err := New(Options{
		ModelDir:   modelDir,
		Model:      "laya-onnx",
		DType:      "fp32",
		EngineName: EngineORT,
		ORTLib:     dll,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eval.Close()
	t.Logf("load_ms=%d engine=%s family=%s", time.Since(start).Milliseconds(), eval.EngineName(), eval.Family())
	if eval.Family() != FamilyLaya {
		t.Fatalf("family = %q", eval.Family())
	}

	state := "I was charged twice for the same order. I want my money back now."
	questions := map[string]systemone.Question{
		"area": systemone.Choice(
			"Which product area is the message about?",
			map[string]string{
				"fees":   "fees & charges",
				"refund": "refund & dispute",
				"card":   "card",
				"other":  "other",
			},
		),
		"ask_refund": systemone.Noul("The customer is asking for a refund."),
	}
	start = time.Now()
	answers, _, err := eval.Ask(context.Background(), state, questions)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	t.Logf("ask_ms=%d answers=%+v", time.Since(start).Milliseconds(), answers)
	if answers["area"].Choice == "" {
		t.Fatalf("area empty: %+v", answers["area"])
	}
	if answers["ask_refund"].Noul < 0.5 {
		t.Fatalf("ask_refund noul=%v", answers["ask_refund"].Noul)
	}
}
