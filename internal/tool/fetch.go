package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// FetchParams is the input schema for the fetch tool.
type FetchParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"` // text, markdown, html
	Timeout int    `json:"timeout,omitempty"`
}

const (
	FetchToolName       = "Fetch"
	MaxFetchSize        = 5 * 1024 * 1024 // 5MB
	DefaultFetchTimeout = 30
	MaxFetchTimeout     = 120
)

type fetchTool struct {
	BaseTool
	client *http.Client
}

// NewFetchTool creates a new URL fetch tool.
func NewFetchTool() Tool {
	return &fetchTool{
		client: &http.Client{Timeout: DefaultFetchTimeout * time.Second},
	}
}

func (f *fetchTool) Name() string                     { return FetchToolName }
func (f *fetchTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (f *fetchTool) Description() string {
	return `Fetches content from a URL and returns it in the specified format (text, markdown, html).
- Provide URL (must start with http:// or https://).
- Format: 'text' (plain text), 'markdown' (HTML converted to Markdown), or 'html' (raw).
- Maximum response size: 5MB.
- Timeout optional (max 120 seconds).`
}

func (f *fetchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The URL to fetch content from",
			},
			"format": map[string]any{
				"type":        "string",
				"enum":        []string{"text", "markdown", "html"},
				"description": "The format to return the content in",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Optional timeout in seconds (max 120)",
			},
		},
		"required": []string{"url", "format"},
	}
}

func (f *fetchTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params FetchParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}

	if params.URL == "" {
		return ErrorResult("url is required"), nil
	}

	format := strings.ToLower(params.Format)
	if format != "text" && format != "markdown" && format != "html" {
		return ErrorResult("format must be one of: text, markdown, html"), nil
	}

	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		return ErrorResult("URL must start with http:// or https://"), nil
	}

	client := f.client
	if params.Timeout > 0 {
		if params.Timeout > MaxFetchTimeout {
			params.Timeout = MaxFetchTimeout
		}
		client = &http.Client{Timeout: time.Duration(params.Timeout) * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", params.URL, nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err)), nil
	}
	req.Header.Set("User-Agent", "codeplus-agent/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to fetch URL: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ErrorResult(fmt.Sprintf("request failed with status code: %d", resp.StatusCode)), nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchSize))
	if err != nil {
		return ErrorResult("failed to read response body: " + err.Error()), nil
	}

	content := string(body)
	contentType := resp.Header.Get("Content-Type")

	switch format {
	case "text":
		if strings.Contains(contentType, "text/html") {
			text := extractTextFromHTML(content)
			return Result(text), nil
		}
		return Result(content), nil

	case "markdown":
		if strings.Contains(contentType, "text/html") {
			markdown, err := convertHTMLToMarkdown(content)
			if err != nil {
				return ErrorResult("failed to convert HTML to Markdown: " + err.Error()), nil
			}
			return Result(markdown), nil
		}
		return Result("```\n" + content + "\n```"), nil

	case "html":
		return Result(content), nil

	default:
		return Result(content), nil
	}
}

func extractTextFromHTML(html string) string {
	// Simple HTML tag stripping
	b := []byte(html)
	var result strings.Builder
	inTag := false
	for _, ch := range b {
		if ch == '<' {
			inTag = true
			continue
		}
		if ch == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteByte(ch)
		}
	}
	// Normalize whitespace
	text := result.String()
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

func convertHTMLToMarkdown(html string) (string, error) {
	return htmltomarkdown.ConvertString(html)
}
