package jevlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/systemone"
)

func TestORTEngineRunOpenJevQ4(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Ensure MinGW gcc is discoverable for CGO in this process.
		os.Setenv("CGO_ENABLED", "1")
	}
	modelDir := os.ExpandEnv(`C:\Users\solosw\.solcode\models\open-jev-deberta-v3-large`)
	dll := DefaultORTLibrary()
	if _, err := os.Stat(dll); err != nil {
		t.Skip("ort dll missing:", err)
	}
	onnx := filepath.Join(modelDir, "onnx", "model_q4.onnx")
	if _, err := os.Stat(onnx); err != nil {
		t.Skip("open-jev q4 missing:", err)
	}

	start := time.Now()
	eng, err := NewORTEngine(ORTOptions{ModelPath: onnx, SharedLibrary: dll})
	if err != nil {
		t.Fatalf("NewORTEngine: %v", err)
	}
	defer eng.Close()
	t.Logf("load_ms=%d", time.Since(start).Milliseconds())
	if !eng.Ready() || eng.Name() != EngineORT {
		t.Fatalf("engine ready/name = %v/%q", eng.Ready(), eng.Name())
	}

	seq, pairs := 32, 2
	bundle := TensorBundle{
		InputIDs:      make([]int64, seq),
		AttentionMask: make([]int64, seq),
		Seg:           make([]int64, seq),
		PairQ:         []int64{2, 2},
		PairOpt:       []int64{0, 1},
	}
	for i := 0; i < seq; i++ {
		bundle.InputIDs[i] = 1
		bundle.AttentionMask[i] = 1
		bundle.Seg[i] = -1
	}

	start = time.Now()
	outs, err := eng.RunNamed(context.Background(), namedFromOpenJev(bundle))
	if err != nil {
		t.Fatalf("RunNamed: %v", err)
	}
	logits := openJevLogits(outs)
	t.Logf("run1_ms=%d logits=%v", time.Since(start).Milliseconds(), logits)
	if len(logits) != pairs {
		t.Fatalf("logits len=%d want %d", len(logits), pairs)
	}

	start = time.Now()
	const n = 3
	for i := 0; i < n; i++ {
		if _, err := eng.RunNamed(context.Background(), namedFromOpenJev(bundle)); err != nil {
			t.Fatalf("RunNamed[%d]: %v", i, err)
		}
	}
	t.Logf("run_avg_ms=%d", time.Since(start).Milliseconds()/n)
}

func TestDefaultEngineORTRequiresLibrary(t *testing.T) {
	dir := t.TempDir()
	writeMinimalOpenJev(t, dir, "q4")
	arts, err := ResolveArtifacts(dir, "q4", "t")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := defaultEngine(arts, Options{EngineName: EngineORT, ORTLib: filepath.Join(dir, "missing.dll")})
	if err != nil {
		t.Fatalf("async defaultEngine should return immediately: %v", err)
	}
	loader, ok := eng.(*loadingORTEngine)
	if !ok {
		t.Fatalf("engine type = %T, want *loadingORTEngine", eng)
	}
	defer eng.Close()
	loader.Wait()
	if loader.Ready() {
		t.Fatal("missing library should leave engine not Ready")
	}
	if loader.LoadError() == nil {
		t.Fatal("expected missing library load error")
	}
}

func TestDefaultEngineORTStartsAsync(t *testing.T) {
	dir := t.TempDir()
	writeMinimalOpenJev(t, dir, "q4")
	arts, err := ResolveArtifacts(dir, "q4", "t")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	eng, err := defaultEngine(arts, Options{EngineName: EngineORT, ORTLib: filepath.Join(dir, "missing.dll")})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("defaultEngine blocked for %s, want async return", time.Since(start))
	}
	if eng.Name() != EngineORT {
		t.Fatalf("Name = %q", eng.Name())
	}
	if eng.Ready() {
		t.Fatal("async engine should not be Ready immediately with a missing lib")
	}
	_, runErr := eng.RunNamed(context.Background(), NamedTensors{})
	if !errors.Is(runErr, ErrEngineNotReady) {
		t.Fatalf("RunNamed = %v, want ErrEngineNotReady", runErr)
	}
}

func TestLocalEvaluatorAskORTEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		os.Setenv("CGO_ENABLED", "1")
	}
	modelDir := os.ExpandEnv(`C:\Users\solosw\.solcode\models\open-jev-deberta-v3-large`)
	dll := DefaultORTLibrary()
	if _, err := os.Stat(dll); err != nil {
		t.Skip("ort dll missing:", err)
	}
	if _, err := os.Stat(filepath.Join(modelDir, "onnx", "model_q4.onnx")); err != nil {
		t.Skip("open-jev q4 missing:", err)
	}
	if _, err := os.Stat(filepath.Join(modelDir, "spm.model")); err != nil {
		t.Skip("open-jev spm missing:", err)
	}

	start := time.Now()
	eval, err := New(Options{
		ModelDir:   modelDir,
		Model:      "open-jev-deberta-v3-large",
		DType:      "q4",
		EngineName: EngineORT,
		ORTLib:     dll,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eval.Close()
	t.Logf("new_ms=%d engine=%s ready=%v", time.Since(start).Milliseconds(), eval.EngineName(), eval.EngineReady())
	waitStart := time.Now()
	eval.WaitEngine()
	t.Logf("load_wait_ms=%d ready=%v", time.Since(waitStart).Milliseconds(), eval.EngineReady())
	if !eval.EngineReady() {
		t.Fatal("ORT engine not ready after WaitEngine")
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
		t.Fatalf("area choice empty: %+v", answers["area"])
	}
	// Noul on the card example is stable under q4; choice can drift with
	// quantization so only require a non-empty label there.
	if answers["ask_refund"].Noul < 0.5 {
		t.Fatalf("ask_refund noul=%v, want >= 0.5", answers["ask_refund"].Noul)
	}
	if answers["area"].Choice != "refund" {
		t.Logf("note: area chose %q (card example prefers refund; q4 may differ)", answers["area"].Choice)
	}
}
