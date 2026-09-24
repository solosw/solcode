package embedding

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/philippgille/chromem-go"
	"github.com/solosw/solcode/internal/config"
)

// Provider turns text into a normalized embedding vector.
type Provider interface {
	// Embed embeds a query-style string (used for retrieval).
	Embed(ctx context.Context, text string) ([]float32, error)
	Close() error
}

// DocumentEmbedder optionally embeds document/index text with a different
// prompt prefix than queries (EmbeddingGemma). API providers typically reuse Embed.
type DocumentEmbedder interface {
	EmbedDocument(ctx context.Context, text string) ([]float32, error)
}

// Options configures a Provider from EmbeddingConfig.
type Options struct {
	Config config.EmbeddingConfig
	// ModelDir overrides shared/project model discovery for local backends.
	ModelDir string
	// ORTLib overrides the default ~/.solcode/lib onnxruntime shared library.
	ORTLib string
	// GPU enables CUDA EP for the local ORT session (shared with Jev via ort{}).
	GPU bool
	// CudaDeviceID selects the CUDA device when GPU is true (default 0).
	CudaDeviceID int
}

// NewProvider builds an API or local embedding backend from cfg.
func NewProvider(opts Options) (Provider, error) {
	cfg := opts.Config
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case config.EmbeddingBackendLocal:
		return newLocalProvider(opts)
	default:
		return newAPIProvider(opts)
	}
}

// EmbeddingFunc adapts a Provider to chromem-go's EmbeddingFunc.
func EmbeddingFunc(p Provider) chromem.EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		if p == nil {
			return nil, fmt.Errorf("embedding provider is nil")
		}
		return p.Embed(ctx, text)
	}
}

func timeoutFrom(cfg config.EmbeddingConfig) time.Duration {
	sec := cfg.TimeoutSec
	if sec <= 0 {
		sec = 30
	}
	if sec > 300 {
		sec = 300
	}
	return time.Duration(sec) * time.Second
}

// truncateAndNormalize applies Matryoshka-style truncation then L2 renorm.
// dims<=0 leaves the vector unchanged aside from ensuring unit length.
func truncateAndNormalize(v []float32, dims int) []float32 {
	if len(v) == 0 {
		return v
	}
	if dims > 0 && dims < len(v) {
		out := make([]float32, dims)
		copy(out, v[:dims])
		v = out
	} else {
		out := make([]float32, len(v))
		copy(out, v)
		v = out
	}
	return l2Normalize(v)
}

func l2Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return v
}
