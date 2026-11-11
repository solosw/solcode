package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/httpproxy"
)

const providerModelsTimeout = 5 * time.Second

// MarshalJSON keeps transient catalog entries out of settings saved by the TUI
// and web settings. Explicit edits promote a discovered entry to local settings.
func (p ProviderConfig) MarshalJSON() ([]byte, error) {
	type plain ProviderConfig
	local := plain(p)
	local.Models = make([]ModelConfig, 0, len(p.Models))
	for _, model := range p.Models {
		if !model.discovered {
			local.Models = append(local.Models, model)
		}
	}
	return json.Marshal(local)
}

// KeepLocal makes an explicitly edited catalog model a persistent local override.
func (m *ModelConfig) KeepLocal() { m.discovered = false }

func (cfg *Config) discoverProviderModels(ctx context.Context) {
	httpproxy.Apply(cfg.Proxy)
	client := httpproxy.NewClient(providerModelsTimeout)
	defer client.CloseIdleConnections()
	// Refresh every provider with bounded concurrency and a per-request timeout.
	providers := append([]ProviderConfig(nil), cfg.Providers...)
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i := range providers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			p := providers[i]
			if v := os.Getenv(p.APIKeyEnv); p.APIKeyEnv != "" && v != "" {
				p.APIKey = v
			}
			if v := os.Getenv(p.BaseURLEnv); p.BaseURLEnv != "" && v != "" {
				p.BaseURL = v
			}
			format := p.APIFormat
			if format == "" {
				format = cfg.APIFormat
			}
			// An unconfigured default provider should still allow first-run setup.
			if strings.TrimSpace(p.BaseURL) == "" && strings.TrimSpace(p.APIKey) == "" {
				return
			}
			models, err := fetchProviderModels(ctx, client, p, format)
			if err != nil {
				return
			} // Local models remain usable offline or on unsupported endpoints.
			providers[i].Models = mergeProviderModels(p.Name, p.Models, models)
		}(i)
	}
	wg.Wait()
	cfg.Providers = providers
}

func providerModelsEndpoint(baseURL, format string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		if anthropic.NormalizeFormat(format) == anthropic.FormatAnthropic {
			baseURL = "https://api.anthropic.com"
		} else {
			baseURL = "https://api.openai.com"
		}
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid provider base URL")
	}
	path := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/messages", "/responses", "/models"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			break
		}
	}
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	u.Path, u.RawPath, u.Fragment = path+"/models", "", ""
	return u.String(), nil
}

func fetchProviderModels(ctx context.Context, client *http.Client, provider ProviderConfig, format string) ([]ModelConfig, error) {
	endpoint, err := providerModelsEndpoint(provider.BaseURL, format)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if provider.APIKey != "" {
		if anthropic.NormalizeFormat(format) == anthropic.FormatAnthropic {
			req.Header.Set("x-api-key", provider.APIKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+provider.APIKey)
		}
	}
	if anthropic.NormalizeFormat(format) == anthropic.FormatAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models request: HTTP %d", resp.StatusCode)
	}
	var catalog struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&catalog); err != nil {
		return nil, err
	}
	models := make([]ModelConfig, 0, len(catalog.Data))
	for _, m := range catalog.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		models = append(models, ModelConfig{Name: id, ID: id, DisplayName: m.DisplayName, Provider: provider.Name, discovered: true})
	}
	return models, nil
}

func mergeProviderModels(provider string, local, remote []ModelConfig) []ModelConfig {
	merged := append([]ModelConfig(nil), local...)
	seen := make(map[string]bool, len(local)+len(remote))
	for _, m := range local {
		seen[m.Name], seen[m.ID] = true, true
	}
	for _, m := range remote {
		if m.ID == "" || seen[m.Name] || seen[m.ID] {
			continue
		}
		m.Provider, m.discovered = provider, true
		merged = append(merged, m)
		seen[m.Name], seen[m.ID] = true, true
	}
	return merged
}
