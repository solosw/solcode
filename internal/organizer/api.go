package organizer

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/config"
)

// APIGenerator uses only the explicitly configured organizer endpoint. It never
// inherits chat-provider credentials or falls back from local inference.
type APIGenerator struct {
	client *anthropic.Client
	model  string
	ready  bool
}

func NewAPIGenerator(cfg config.OrganizerConfig) *APIGenerator {
	format := cfg.APIFormat
	if strings.TrimSpace(format) == "" {
		format = anthropic.FormatOpenAI
	}
	// The shared chat client appends /v1 itself for Messages and Chat
	// Completions. Accept either a provider origin or its /v1 API root.
	baseURL := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"), "/v1")
	return &APIGenerator{
		client: anthropic.NewClient(anthropic.Options{APIKey: cfg.APIKey, BaseURL: baseURL, Format: format}),
		model:  strings.TrimSpace(cfg.Model),
		ready:  strings.TrimSpace(cfg.Model) != "" && strings.TrimSpace(cfg.BaseURL) != "" && strings.TrimSpace(cfg.APIKey) != "",
	}
}

func (g *APIGenerator) Name() string { return "api" }
func (g *APIGenerator) Ready() bool  { return g != nil && g.ready }
func (g *APIGenerator) Close() error { return nil }
func (g *APIGenerator) Generate(ctx context.Context, req GenerateRequest) (string, error) {
	if !g.Ready() {
		return "", ErrUnavailable
	}
	response, err := g.client.Create(ctx, anthropic.MessageRequest{
		Model: g.model, MaxTokens: int64(req.MaxTokens), System: req.System,
		Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(req.User))},
	})
	if err != nil {
		return "", fmt.Errorf("organizer api: %w", err)
	}
	if response.StopReason == sdk.StopReasonMaxTokens {
		return "", fmt.Errorf("organizer api: output truncated at token limit")
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", fmt.Errorf("organizer api: empty response")
	}
	return text.String(), nil
}

var _ LocalGenerator = (*APIGenerator)(nil)
