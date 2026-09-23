package embedding

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/config"
)

func TestTruncateAndNormalize(t *testing.T) {
	v := []float32{3, 4}
	got := truncateAndNormalize(v, 0)
	if math.Abs(float64(got[0])-0.6) > 1e-5 || math.Abs(float64(got[1])-0.8) > 1e-5 {
		t.Fatalf("got %v", got)
	}
	// original unchanged
	if v[0] != 3 {
		t.Fatalf("input mutated: %v", v)
	}
	long := []float32{1, 0, 0, 0}
	got = truncateAndNormalize(long, 2)
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if math.Abs(float64(got[0])-1) > 1e-5 {
		t.Fatalf("got %v", got)
	}
}

func TestResolveLocalArtifactsShared(t *testing.T) {
	shared := config.SharedEmbeddingModelDir()
	onnx := filepath.Join(shared, "model_q4f16.onnx")
	if _, err := os.Stat(onnx); err != nil {
		t.Skip("shared EmbeddingGemma missing:", err)
	}
	arts, err := ResolveLocalArtifacts("", t.TempDir(), "embeddinggemma-300m")
	if err != nil {
		t.Fatal(err)
	}
	if arts.ONNXPath != onnx {
		t.Fatalf("onnx=%q want %q", arts.ONNXPath, onnx)
	}
	if arts.ModelID != "embeddinggemma-300m" {
		t.Fatalf("model=%q", arts.ModelID)
	}
}

func TestDefaultEmbeddingDirSiblingOfKnowledge(t *testing.T) {
	work := `C:\software\projects\demo`
	got := config.DefaultEmbeddingDir(work)
	know := config.DefaultKnowledgeGraphPath(work)
	if filepath.Dir(got) != filepath.Dir(know) {
		t.Fatalf("dir parent mismatch: emb=%q know=%q", got, know)
	}
	if filepath.Base(got) != "embeddings" {
		t.Fatalf("base=%q", filepath.Base(got))
	}
}
