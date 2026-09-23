package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/solosw/solcode/internal/httpproxy"
)

type apiProvider struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	client     *http.Client
}

func newAPIProvider(opts Options) (*apiProvider, error) {
	cfg := opts.Config
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, fmt.Errorf("embedding api: api_key is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("embedding api: model is required")
	}
	return &apiProvider{
		baseURL:    base,
		apiKey:     key,
		model:      model,
		dimensions: cfg.Dimensions,
		client:     httpproxy.NewClient(timeoutFrom(cfg)),
	}, nil
}

func (p *apiProvider) Close() error { return nil }

type embeddingsRequest struct {
	Input      string `json:"input"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type embeddingsResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *apiProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if p == nil {
		return nil, fmt.Errorf("embedding api provider is nil")
	}
	reqBody := embeddingsRequest{
		Input: text,
		Model: p.model,
	}
	if p.dimensions > 0 {
		reqBody.Dimensions = p.dimensions
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	url := p.baseURL + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding api: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var parsed embeddingsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("embedding api decode: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("embedding api: %s", parsed.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding api: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedding api: empty embedding")
	}
	// OpenAI vectors are already normalized; keep truncateAndNormalize for safety
	// when dimensions truncation was requested by the provider itself.
	return truncateAndNormalize(parsed.Data[0].Embedding, 0), nil
}

func (p *apiProvider) EmbedDocument(ctx context.Context, text string) ([]float32, error) {
	return p.Embed(ctx, text)
}

var (
	_ Provider         = (*apiProvider)(nil)
	_ DocumentEmbedder = (*apiProvider)(nil)
)
