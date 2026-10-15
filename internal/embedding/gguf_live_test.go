package embedding

import (
	"context"
	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/organizer/yzma"
	"math"
	"os"
	"testing"
	"time"
)

func TestGGUFLiveGPU(t *testing.T) {
	name := os.Getenv("SOLCODE_EMBEDDING_GGUF_TEST")
	if name == "" {
		t.Skip("set SOLCODE_EMBEDDING_GGUF_TEST to filename under models")
	}
	cfg := config.Default()
	cfg.Memory.Embedding = config.EmbeddingConfig{Type: "gguf", Model: name}
	cfg.NormalizeLocalModels()
	if err := yzma.EnsureRuntime(""); err != nil {
		t.Fatal(err)
	}
	llama.LogSet(0)
	defer llama.LogSet(llama.LogSilent())
	p, err := NewProvider(Options{Config: cfg.Memory.Embedding})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	v, err := p.Embed(ctx, "How do I run the unit tests?")
	if err != nil {
		t.Fatal(err)
	}
	norm := 0.0
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if len(v) == 0 || math.IsNaN(norm) || math.Abs(norm-1) > 1e-4 {
		t.Fatalf("dimensions=%d norm=%v", len(v), norm)
	}
	t.Logf("GPU embedding: dimensions=%d norm=%.6f", len(v), norm)
}
