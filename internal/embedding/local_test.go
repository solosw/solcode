package embedding

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/jevlocal"
)

func TestLocalProviderEmbedEndToEnd(t *testing.T) {
	shared := config.SharedEmbeddingModelDir()
	if _, err := os.Stat(filepath.Join(shared, "model_q4f16.onnx")); err != nil {
		t.Skip("shared model missing:", err)
	}
	dll := jevlocal.DefaultORTLibrary()
	if _, err := os.Stat(dll); err != nil {
		t.Skip("ort library missing:", err)
	}

	p, err := NewProvider(Options{
		Config: config.EmbeddingConfig{
			Type:       config.EmbeddingBackendLocal,
			Model:      "embeddinggemma-300m",
			Dir:        t.TempDir(),
			TimeoutSec: 120,
			Dimensions: 256,
		},
		ModelDir: shared,
		ORTLib:   dll,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	vec, err := p.Embed(ctx, "Which planet is known as the Red Planet?")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 256 {
		t.Fatalf("dim=%d want 256", len(vec))
	}
	var sum float64
	for _, x := range vec {
		sum += float64(x) * float64(x)
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-3 {
		t.Fatalf("l2=%f", math.Sqrt(sum))
	}
}

func TestStoreAddQueryWithPrecomputed(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	emb := l2Normalize([]float32{1, 0, 0, 0})
	ctx := context.Background()
	if err := s.Add(ctx, "a", "alpha", map[string]string{"k": "v"}, emb); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(ctx, "b", "beta", nil, l2Normalize([]float32{0, 1, 0, 0})); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 2 {
		t.Fatalf("count=%d", s.Count())
	}
	res, err := s.QueryEmbedding(ctx, emb, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ID != "a" {
		t.Fatalf("res=%+v", res)
	}
}
