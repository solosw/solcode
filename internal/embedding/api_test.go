package embedding

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/solosw/solcode/internal/config"
)

func TestEmbeddingAPIUsesIndependentEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer embedding-key" {
			t.Errorf("incorrect endpoint or credentials")
		}
		var body embeddingsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "vendor/embedding" || body.Input != "memory text" || body.Dimensions != 2 {
			t.Errorf("request = %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[3,4]}]}`))
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.Memory.Embedding = config.EmbeddingConfig{Enabled: true, Type: "api", BaseURL: srv.URL + "/v1/", APIKey: "embedding-key", Model: "vendor/embedding", Dimensions: 2}
	cfg.NormalizeLocalModels()
	provider, err := NewProvider(Options{Config: cfg.Memory.Embedding})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	vector, err := provider.Embed(context.Background(), "memory text")
	if err != nil || len(vector) != 2 {
		t.Fatalf("Embed = %v, %v", vector, err)
	}
	if math.Abs(float64(vector[0])-0.6) > 1e-5 || math.Abs(float64(vector[1])-0.8) > 1e-5 {
		t.Fatalf("vector = %v", vector)
	}
}
