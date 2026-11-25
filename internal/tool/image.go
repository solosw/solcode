package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/solosw/solcode/internal/httpproxy"
)

const (
	ImageGenerateToolName = "ImageGenerate"
	ImageEditToolName     = "ImageEdit"

	defaultImageTimeoutSec = 120
	maxImageTimeoutSec     = 600
	maxImageAPIBody        = 40 << 20 // 40MB response ceiling
	defaultImageOutputDir  = ".solcode/images"
)

// ImageAPIConfig is the runtime client settings for OpenAI-format image APIs.
// Configured independently from the chat LLM provider.
type ImageAPIConfig struct {
	BaseURL    string
	APIKey     string
	Model      string
	EditModel  string
	Size       string
	Quality    string
	TimeoutSec int
	OutputDir  string // relative to workdir or absolute
}

// ImageGenerateParams is the model-facing input for image generation.
type ImageGenerateParams struct {
	Prompt  string `json:"prompt"`
	Model   string `json:"model,omitempty"`
	Size    string `json:"size,omitempty"`
	Quality string `json:"quality,omitempty"`
	N       int    `json:"n,omitempty"`
	// Path is optional output path (png). Default: <output_dir>/gen-<ts>.png
	Path string `json:"path,omitempty"`
}

// ImageEditParams is the model-facing input for image edits.
type ImageEditParams struct {
	// Image is the source image path (png preferred for mask edits).
	Image string `json:"image"`
	// Mask optional transparent PNG path (same size as image).
	Mask    string `json:"mask,omitempty"`
	Prompt  string `json:"prompt"`
	Model   string `json:"model,omitempty"`
	Size    string `json:"size,omitempty"`
	N       int    `json:"n,omitempty"`
	Path    string `json:"path,omitempty"`
}

type imageAPIClient struct {
	cfg    ImageAPIConfig
	client *http.Client
}

func newImageAPIClient(cfg ImageAPIConfig) *imageAPIClient {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.EditModel = strings.TrimSpace(cfg.EditModel)
	if cfg.EditModel == "" {
		cfg.EditModel = cfg.Model
	}
	cfg.Size = strings.TrimSpace(cfg.Size)
	cfg.Quality = strings.TrimSpace(cfg.Quality)
	cfg.OutputDir = strings.TrimSpace(cfg.OutputDir)
	if cfg.OutputDir == "" {
		cfg.OutputDir = defaultImageOutputDir
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = defaultImageTimeoutSec
	}
	if cfg.TimeoutSec > maxImageTimeoutSec {
		cfg.TimeoutSec = maxImageTimeoutSec
	}
	return &imageAPIClient{
		cfg:    cfg,
		client: httpproxy.NewClient(time.Duration(cfg.TimeoutSec) * time.Second),
	}
}

func (c *imageAPIClient) endpoint(suffix string) string {
	base := c.cfg.BaseURL
	// Accept either https://host or https://host/v1
	if strings.HasSuffix(base, "/v1") {
		return base + suffix
	}
	return base + "/v1" + suffix
}

type openAIImageResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		B64JSON string `json:"b64_json"`
		URL     string `json:"url"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (c *imageAPIClient) generate(ctx context.Context, prompt, model, size, quality string, n int) ([]byte, error) {
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	body := map[string]any{
		"prompt": prompt,
		"n":      n,
		// Prefer base64 so we can write files without a second fetch.
		"response_format": "b64_json",
	}
	if model != "" {
		body["model"] = model
	}
	if size != "" {
		body["size"] = size
	}
	if quality != "" {
		body["quality"] = quality
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/images/generations"), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	return c.doImage(req)
}

func (c *imageAPIClient) edit(ctx context.Context, imagePath, maskPath, prompt, model, size string, n int) ([]byte, error) {
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := writeMultipartFile(w, "image", imagePath); err != nil {
		return nil, err
	}
	if strings.TrimSpace(maskPath) != "" {
		if err := writeMultipartFile(w, "mask", maskPath); err != nil {
			return nil, err
		}
	}
	_ = w.WriteField("prompt", prompt)
	_ = w.WriteField("n", fmt.Sprintf("%d", n))
	_ = w.WriteField("response_format", "b64_json")
	if model != "" {
		_ = w.WriteField("model", model)
	}
	if size != "" {
		_ = w.WriteField("size", size)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/images/edits"), &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	return c.doImage(req)
}

func writeMultipartFile(w *multipart.Writer, field, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	mimeType := imageMIMEFromPath(path)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filepath.Base(path)))
	h.Set("Content-Type", mimeType)
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, f)
	return err
}

func imageMIMEFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}

func (c *imageAPIClient) doImage(req *http.Request) ([]byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImageAPIBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var parsed openAIImageResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("image API HTTP %d: %s", resp.StatusCode, truncateErrBody(raw))
		}
		return nil, fmt.Errorf("decode image API response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("image API error: %s", parsed.Error.Message)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("image API HTTP %d: %s", resp.StatusCode, truncateErrBody(raw))
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("image API returned no data")
	}
	item := parsed.Data[0]
	if item.B64JSON != "" {
		bin, err := base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("decode b64_json: %w", err)
		}
		return bin, nil
	}
	if item.URL != "" {
		return c.fetchURL(req.Context(), item.URL)
	}
	return nil, fmt.Errorf("image API item missing b64_json and url")
}

func (c *imageAPIClient) fetchURL(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image URL HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxImageAPIBody))
}

func truncateErrBody(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}

func resolveImageOutPath(uctx *UseContext, cfg ImageAPIConfig, explicit, prefix string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	var out string
	if explicit != "" {
		out = ResolvePath(uctx, explicit)
	} else {
		name := fmt.Sprintf("%s-%d.png", prefix, time.Now().Unix())
		rel := filepath.Join(cfg.OutputDir, name)
		out = ResolvePath(uctx, rel)
	}
	if err := CheckAllowedPath(uctx, out); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}
	return out, nil
}

func saveImageBytes(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func imageResultCaption(path string, prompt string, kind string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s image saved.\n", kind)
	fmt.Fprintf(&b, "path: %s\n", path)
	if prompt != "" {
		fmt.Fprintf(&b, "prompt: %s\n", prompt)
	}
	fmt.Fprintf(&b, "Use View with path %q to inspect the result.", path)
	return strings.TrimSpace(b.String())
}

// --- ImageGenerate ---

type imageGenerateTool struct {
	BaseTool
	api *imageAPIClient
}

// NewImageGenerateTool creates the OpenAI-format image generation tool.
func NewImageGenerateTool(cfg ImageAPIConfig) Tool {
	return &imageGenerateTool{api: newImageAPIClient(cfg)}
}

func (t *imageGenerateTool) Name() string                             { return ImageGenerateToolName }
func (t *imageGenerateTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *imageGenerateTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *imageGenerateTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *imageGenerateTool) Description() string {
	defModel := t.api.cfg.Model
	if defModel == "" {
		defModel = "(provider default)"
	}
	return fmt.Sprintf(`Generate an image from a text prompt via the OpenAI Images API (/v1/images/generations).
Saves a PNG under the workdir (default %s/) and returns the path.
- prompt (required): what to draw
- model: optional override (default %s)
- size: optional e.g. 1024x1024, 1792x1024
- quality: optional provider-specific (standard, hd, …)
- path: optional output path relative to workdir
Requires settings.json "image" { base_url, api_key } (independent of chat provider).`,
		defaultImageOutputDir, defModel)
}

func (t *imageGenerateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Text description of the image to generate",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Image model id override",
			},
			"size": map[string]any{
				"type":        "string",
				"description": "Image size, e.g. 1024x1024",
			},
			"quality": map[string]any{
				"type":        "string",
				"description": "Optional quality (provider-specific)",
			},
			"n": map[string]any{
				"type":        "integer",
				"description": "Number of images (only first is saved; default 1, max 4)",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional output file path (png) under the workdir",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *imageGenerateTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params ImageGenerateParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	prompt := strings.TrimSpace(params.Prompt)
	if prompt == "" {
		return ErrorResult("prompt is required"), nil
	}
	model := strings.TrimSpace(params.Model)
	if model == "" {
		model = t.api.cfg.Model
	}
	size := strings.TrimSpace(params.Size)
	if size == "" {
		size = t.api.cfg.Size
	}
	quality := strings.TrimSpace(params.Quality)
	if quality == "" {
		quality = t.api.cfg.Quality
	}
	outPath, err := resolveImageOutPath(uctx, t.api.cfg, params.Path, "gen")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	bin, err := t.api.generate(ctx, prompt, model, size, quality, params.N)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	if err := saveImageBytes(outPath, bin); err != nil {
		return ErrorResult("write image: " + err.Error()), nil
	}
	return Result(imageResultCaption(outPath, prompt, "Generated")), nil
}

// --- ImageEdit ---

type imageEditTool struct {
	BaseTool
	api *imageAPIClient
}

// NewImageEditTool creates the OpenAI-format image edit tool.
func NewImageEditTool(cfg ImageAPIConfig) Tool {
	return &imageEditTool{api: newImageAPIClient(cfg)}
}

func (t *imageEditTool) Name() string                             { return ImageEditToolName }
func (t *imageEditTool) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *imageEditTool) IsDestructive(_ json.RawMessage) bool     { return false }
func (t *imageEditTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *imageEditTool) Description() string {
	defModel := t.api.cfg.EditModel
	if defModel == "" {
		defModel = "(provider default)"
	}
	return fmt.Sprintf(`Edit or extend an existing image with a prompt via the OpenAI Images API (/v1/images/edits).
- image (required): path to source image (png recommended)
- prompt (required): edit instruction
- mask: optional transparent PNG mask (same dimensions)
- model: optional override (default %s)
- size: optional
- path: optional output path
Saves result under workdir and returns the path. Requires settings.json "image" config.`, defModel)
}

func (t *imageEditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image": map[string]any{
				"type":        "string",
				"description": "Path to the source image to edit",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "How to edit or extend the image",
			},
			"mask": map[string]any{
				"type":        "string",
				"description": "Optional mask image path (transparent areas are editable)",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Image edit model id override",
			},
			"size": map[string]any{
				"type":        "string",
				"description": "Output size, e.g. 1024x1024",
			},
			"n": map[string]any{
				"type":        "integer",
				"description": "Number of images (only first is saved; default 1, max 4)",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional output file path under the workdir",
			},
		},
		"required": []string{"image", "prompt"},
	}
}

func (t *imageEditTool) Invoke(ctx context.Context, uctx *UseContext, input json.RawMessage) (*ContentBlock, error) {
	var params ImageEditParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ErrorResult("invalid parameters: " + err.Error()), nil
	}
	prompt := strings.TrimSpace(params.Prompt)
	if prompt == "" {
		return ErrorResult("prompt is required"), nil
	}
	if strings.TrimSpace(params.Image) == "" {
		return ErrorResult("image is required"), nil
	}
	src := ResolvePath(uctx, params.Image)
	if err := CheckAllowedPath(uctx, src); err != nil {
		return ErrorResult(err.Error()), nil
	}
	if _, err := os.Stat(src); err != nil {
		return ErrorResult(fmt.Sprintf("image not found: %s", src)), nil
	}
	mask := ""
	if strings.TrimSpace(params.Mask) != "" {
		mask = ResolvePath(uctx, params.Mask)
		if err := CheckAllowedPath(uctx, mask); err != nil {
			return ErrorResult(err.Error()), nil
		}
		if _, err := os.Stat(mask); err != nil {
			return ErrorResult(fmt.Sprintf("mask not found: %s", mask)), nil
		}
	}
	model := strings.TrimSpace(params.Model)
	if model == "" {
		model = t.api.cfg.EditModel
	}
	size := strings.TrimSpace(params.Size)
	if size == "" {
		size = t.api.cfg.Size
	}
	outPath, err := resolveImageOutPath(uctx, t.api.cfg, params.Path, "edit")
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	bin, err := t.api.edit(ctx, src, mask, prompt, model, size, params.N)
	if err != nil {
		return ErrorResult(err.Error()), nil
	}
	if err := saveImageBytes(outPath, bin); err != nil {
		return ErrorResult("write image: " + err.Error()), nil
	}
	return Result(imageResultCaption(outPath, prompt, "Edited")), nil
}
