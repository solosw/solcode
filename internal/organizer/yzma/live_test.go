//go:build live

package yzma

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/solosw/solcode/internal/organizer"
)

// livePaths resolves the MiniCPM GGUF and llama.cpp library used by the
// developer's machine. The test is skipped when either is missing so CI and
// machines without a local model stay green.
func livePaths(t *testing.T) (modelPath, libDir string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	modelPath = os.Getenv("SOLCODE_ORGANIZER_MODEL")
	if modelPath == "" {
		modelPath = filepath.Join(home, ".solcode", "models", "MiniCPM5-1B-Q4_K_M.gguf")
	}
	libDir = os.Getenv("YZMA_LIB")
	if libDir == "" {
		libDir = filepath.Join(home, ".solcode", "lib", "llama")
	}
	if !ModelPresent(modelPath) {
		t.Skipf("MiniCPM GGUF not found at %s (set SOLCODE_ORGANIZER_MODEL)", modelPath)
	}
	if !LibraryPresent(libDir) {
		t.Skipf("llama.cpp library not found in %s (set YZMA_LIB)", libDir)
	}
	return modelPath, libDir
}

func waitReady(t *testing.T, gen *generator, timeout time.Duration) {
	t.Helper()
	// New no longer preloads; kick the demand-load path explicitly.
	if err := gen.waitForReady(context.Background(), timeout); err != nil {
		t.Fatalf("model load failed: %v", err)
	}
}

// TestLiveMiniCPMOrganize is the end-to-end probe for the organizer path on a
// real MiniCPM GGUF. It always dumps raw model output so an empty or non-XML
// completion is diagnosable without re-running.
func TestLiveMiniCPMOrganize(t *testing.T) {
	modelPath, libDir := livePaths(t)

	gpuLayers := -1 // offload all layers when a CUDA build is installed
	if v := strings.TrimSpace(os.Getenv("SOLCODE_ORGANIZER_GPU_LAYERS")); v != "" {
		fmt.Sscanf(v, "%d", &gpuLayers)
	}

	gen := New(Config{
		ModelPath:   modelPath,
		LibDir:      libDir,
		ContextSize: 16384,
		GPULayers:   gpuLayers,
	})
	t.Cleanup(func() { _ = gen.Close() })
	ref := gen.(*generator)
	t.Logf("loading MiniCPM from %s (lib=%s gpu_layers=%d)", modelPath, libDir, gpuLayers)
	waitReady(t, ref, 3*time.Minute)
	t.Logf("model ready")

	// Probe chat-template rendering without generation, so a template failure
	// is visible even when generation returns empty.
	prompt, usedTemplate := ref.applyChatTemplate(
		"Return ONLY one XML document rooted at <result>.",
		`{"transcript":"user: implement organizer\nassistant: done"}`,
	)
	t.Logf("chat_template_ok=%v prompt_bytes=%d prompt_preview=%q",
		usedTemplate, len(prompt), trimForLog(prompt, 400))
	if strings.TrimSpace(prompt) == "" {
		t.Fatal("chat template rendered an empty prompt")
	}

	org := organizer.New(gen, organizer.Options{
		MaxOutputTokens: 600,
		Temperature:     0,
		TimeoutSec:      300,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Direct Generate first so raw output is visible even when Organize fails
	// at ParseResult.
	raw, err := gen.Generate(ctx, organizer.GenerateRequest{
		System:      "Return ONLY one XML document. No prose. Root element must be <result> with <session_summary>, <keywords>, <importance>, <candidate_memories>.",
		User:        `{"session_id":"live","transcript":"user: wire the local organizer\nassistant: implemented yzma generator and XML schema\nuser: verify with go test\nassistant: tests passed"}`,
		Grammar:     organizer.OrganizeGrammar(),
		MaxTokens:   600,
		Temperature: 0,
	})
	t.Logf("raw_err=%v raw_bytes=%d raw_utf8=%v raw=%q",
		err, len(raw), utf8.ValidString(raw), trimForLog(raw, 1500))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if strings.TrimSpace(raw) == "" {
		// Second attempt without grammar to separate "grammar broke sampling"
		// from "model produces nothing at all".
		raw2, err2 := gen.Generate(ctx, organizer.GenerateRequest{
			System:      "Reply with the single word READY.",
			User:        "ping",
			MaxTokens:   32,
			Temperature: 0,
		})
		t.Logf("no_grammar_probe err=%v raw=%q", err2, trimForLog(raw2, 200))
		t.Fatal("Generate returned empty output with grammar")
	}

	parsed, err := organizer.ParseResult(raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v\nraw=%q", err, raw)
	}
	t.Logf("parsed summary=%q keywords=%v candidates=%d importance=%v",
		trimForLog(parsed.Summary, 200), parsed.Keywords, len(parsed.Candidates), parsed.Importance)
	if strings.TrimSpace(parsed.Summary) == "" {
		t.Fatal("parsed summary is empty")
	}

	// Full Organize path (grammar + ParseResult + caps).
	result, err := org.Organize(ctx, organizer.Input{
		SessionID:  "live-minicpm",
		Transcript: "user: implement the local organizer\nassistant: done with yzma and XML grammar\nuser: run tests\nassistant: go test ./internal/organizer/... passed",
	})
	if err != nil {
		t.Fatalf("Organize() error = %v", err)
	}
	t.Logf("organize summary=%q keywords=%v candidates=%d model=%s elapsed=%s",
		trimForLog(result.Summary, 200), result.Keywords, len(result.Candidates), result.Model, result.Elapsed)
	if result.Summary == "" {
		t.Fatal("Organize returned empty summary")
	}
}

func trimForLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestLiveMiniCPMSpeed measures load time and generation throughput for the
// local MiniCPM organizer model. It reports:
//
//   - model load wall time
//   - free-text decode tok/s (no GBNF; fixed MaxTokens)
//   - grammar-constrained decode tok/s (organizer XML GBNF)
//   - full Organize() wall time (grammar + parse)
//
// Output tokens are counted by re-tokenizing the completion, which matches what
// llama.cpp emitted more closely than a bytes/4 heuristic.
func TestLiveMiniCPMSpeed(t *testing.T) {
	modelPath, libDir := livePaths(t)

	gpuLayers := -1
	if v := strings.TrimSpace(os.Getenv("SOLCODE_ORGANIZER_GPU_LAYERS")); v != "" {
		fmt.Sscanf(v, "%d", &gpuLayers)
	}

	loadStart := time.Now()
	gen := New(Config{
		ModelPath:   modelPath,
		LibDir:      libDir,
		ContextSize: 4096,
		GPULayers:   gpuLayers,
	})
	t.Cleanup(func() { _ = gen.Close() })
	ref := gen.(*generator)
	waitReady(t, ref, 3*time.Minute)
	loadElapsed := time.Since(loadStart)
	t.Logf("load: model=%s gpu_layers=%d elapsed=%s", filepath.Base(modelPath), gpuLayers, loadElapsed.Round(time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Warmup: one short free-text completion so CUDA graphs / kernels settle
	// before the timed runs.
	if _, err := gen.Generate(ctx, organizer.GenerateRequest{
		System:      "Reply briefly.",
		User:        "Say hi in five words.",
		MaxTokens:   16,
		Temperature: 0,
	}); err != nil {
		t.Fatalf("warmup Generate() error = %v", err)
	}

	countTokens := func(text string) int {
		if strings.TrimSpace(text) == "" {
			return 0
		}
		// parseSpecial=false: count the emitted text as plain pieces, not chat markers.
		return len(llama.Tokenize(ref.vocab, text, false, false))
	}

	type speedRun struct {
		name     string
		grammar  string
		maxTok   int
		system   string
		user     string
		repeats  int
	}
	runs := []speedRun{
		{
			name:    "free_text",
			maxTok:  128,
			repeats: 3,
			system:  "You are a concise assistant. Answer in plain prose only.",
			user:    "Summarize in 3 short sentences: a coding agent wired a local llama.cpp organizer that writes session memories as XML.",
		},
		{
			name:    "grammar_xml",
			grammar: organizer.OrganizeGrammar(),
			maxTok:  400,
			repeats: 3,
			system:  "Return ONLY one XML document. No prose. Root <result> with session_summary, keywords, importance, candidate_memories.",
			user:    `{"session_id":"speed","transcript":"user: wire local organizer\nassistant: implemented yzma + XML grammar\nuser: run go test\nassistant: passed"}`,
		},
	}

	for _, run := range runs {
		var totalOut time.Duration
		var totalTok int
		var totalBytes int
		var lastRaw string
		for i := 0; i < run.repeats; i++ {
			start := time.Now()
			raw, err := gen.Generate(ctx, organizer.GenerateRequest{
				System:      run.system,
				User:        run.user,
				Grammar:     run.grammar,
				MaxTokens:   run.maxTok,
				Temperature: 0,
			})
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("%s[%d] Generate() error = %v", run.name, i, err)
			}
			toks := countTokens(raw)
			totalOut += elapsed
			totalTok += toks
			totalBytes += len(raw)
			lastRaw = raw
			tps := 0.0
			if elapsed > 0 && toks > 0 {
				tps = float64(toks) / elapsed.Seconds()
			}
			t.Logf("%s[%d]: elapsed=%s out_tokens=%d out_bytes=%d tok/s=%.1f",
				run.name, i, elapsed.Round(time.Millisecond), toks, len(raw), tps)
		}
		avgElapsed := totalOut / time.Duration(run.repeats)
		avgTPS := 0.0
		if totalOut > 0 && totalTok > 0 {
			avgTPS = float64(totalTok) / totalOut.Seconds()
		}
		t.Logf("%s AVG over %d runs: elapsed=%s out_tokens=%d (total) out_bytes=%d (total) tok/s=%.1f preview=%q",
			run.name, run.repeats, avgElapsed.Round(time.Millisecond), totalTok, totalBytes, avgTPS, trimForLog(lastRaw, 180))
	}

	// Full organizer path once: wall time the product actually pays after compact.
	org := organizer.New(gen, organizer.Options{
		MaxOutputTokens: 400,
		Temperature:     0,
		TimeoutSec:      180,
	})
	orgStart := time.Now()
	result, err := org.Organize(ctx, organizer.Input{
		SessionID:  "speed-minicpm",
		Transcript: "user: implement the local organizer\nassistant: done with yzma and XML grammar\nuser: run tests\nassistant: go test ./internal/organizer/... passed",
	})
	orgElapsed := time.Since(orgStart)
	// Speed is the goal here; content quality is covered by TestLiveMiniCPMOrganize.
	// Still log parse failures so a total regression is visible without failing the bench.
	if err != nil {
		t.Logf("organize: elapsed=%s error=%v (speed bench continues)", orgElapsed.Round(time.Millisecond), err)
	} else {
		t.Logf("organize: elapsed=%s model=%s summary_bytes=%d keywords=%d candidates=%d summary=%q",
			orgElapsed.Round(time.Millisecond), result.Model, len(result.Summary),
			len(result.Keywords), len(result.Candidates), trimForLog(result.Summary, 160))
	}
	t.Logf("SPEED_SUMMARY load=%s free_text and grammar averages logged above; organize=%s",
		loadElapsed.Round(time.Millisecond), orgElapsed.Round(time.Millisecond))
}
