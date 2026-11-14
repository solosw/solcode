package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

type MessageRequest struct {
	Model     string
	MaxTokens int64
	System    string
	Messages  []sdk.MessageParam
	Tools     []sdk.ToolUnionParam
	// ForceTool requires exactly one call to the named tool.
	ForceTool       string
	Thinking        bool
	ThinkingText    bool
	Effort          string
	Stream          bool
	OnTextDelta     func(string)
	OnThinkingDelta func(string)
	// OnToolInputDelta reports streamed tool arguments without displaying them as chat.
	OnToolInputDelta func(string)
}

// MarshalJSON writes the native Messages API request without using the SDK client.
// SDK block types remain at the public boundary while the wire envelope is owned here.
func (r MessageRequest) MarshalJSON() ([]byte, error) {
	model := strings.TrimSpace(r.Model)
	if model == "" {
		model = DefaultModel
	}
	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16_000
	}
	type cacheControl struct {
		Type string `json:"type"`
		TTL  string `json:"ttl,omitempty"`
	}
	type systemBlock struct {
		Type         string       `json:"type"`
		Text         string       `json:"text"`
		CacheControl cacheControl `json:"cache_control"`
	}
	body := struct {
		Model        string               `json:"model"`
		MaxTokens    int64                `json:"max_tokens"`
		Messages     []sdk.MessageParam   `json:"messages"`
		Tools        []sdk.ToolUnionParam `json:"tools,omitempty"`
		System       []systemBlock        `json:"system,omitempty"`
		ToolChoice   any                  `json:"tool_choice,omitempty"`
		Thinking     any                  `json:"thinking,omitempty"`
		OutputConfig map[string]string    `json:"output_config,omitempty"`
		Stream       bool                 `json:"stream,omitempty"`
		CacheControl cacheControl         `json:"cache_control"`
	}{
		Model:        model,
		MaxTokens:    maxTokens,
		Messages:     r.Messages,
		Tools:        withLastToolCacheBreakpoint(r.Tools),
		Stream:       r.Stream,
		CacheControl: cacheControl{Type: "ephemeral", TTL: "1h"},
	}
	if r.ForceTool != "" {
		body.ToolChoice = map[string]any{"type": "tool", "name": r.ForceTool, "disable_parallel_tool_use": true}
	}
	if r.System != "" {
		body.System = []systemBlock{{Type: "text", Text: r.System, CacheControl: cacheControl{Type: "ephemeral", TTL: "1h"}}}
	}
	if r.Thinking {
		thinking := map[string]any{"type": "adaptive"}
		if r.ThinkingText {
			thinking["display"] = "summarized"
		}
		body.Thinking = thinking
	}
	if effort := strings.TrimSpace(r.Effort); effort != "" {
		body.OutputConfig = map[string]string{"effort": effort}
	}
	return json.Marshal(body)
}

func (r MessageRequest) ToSDKParams() sdk.MessageNewParams {
	model := r.Model
	if model == "" {
		model = DefaultModel
	}
	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16_000
	}
	// Prompt caching is opt-in on the Anthropic Messages API. Many OpenAI-compatible
	// gateways inject cache markers during conversion; the native path must set them
	// itself or cache_creation/cache_read stay 0 even with a stable prefix.
	//
	// Use both:
	//  1) explicit breakpoints on the stable tools+system prefix (what converters usually add)
	//  2) top-level automatic caching so multi-turn history can advance the breakpoint
	params := sdk.MessageNewParams{
		Model:        sdk.Model(model),
		MaxTokens:    maxTokens,
		Messages:     r.Messages,
		Tools:        withLastToolCacheBreakpoint(r.Tools),
		CacheControl: ephemeralCacheControl(),
	}
	if r.ForceTool != "" {
		params.ToolChoice = sdk.ToolChoiceUnionParam{OfTool: &sdk.ToolChoiceToolParam{Name: r.ForceTool, DisableParallelToolUse: sdk.Bool(true)}}
	}
	if r.System != "" {
		params.System = []sdk.TextBlockParam{{
			Text:         r.System,
			CacheControl: ephemeralCacheControl(),
		}}
	}
	if r.Thinking {
		adaptive := sdk.ThinkingConfigAdaptiveParam{}
		if r.ThinkingText {
			adaptive.Display = sdk.ThinkingConfigAdaptiveDisplaySummarized
		}
		params.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
	}
	if r.Effort != "" {
		params.OutputConfig = sdk.OutputConfigParam{Effort: sdk.OutputConfigEffort(r.Effort)}
	}
	return params
}

func ephemeralCacheControl() sdk.CacheControlEphemeralParam {
	// 1h keeps tools/system warm across normal turn gaps; default 5m expires too easily.
	ctrl := sdk.NewCacheControlEphemeralParam()
	ctrl.TTL = sdk.CacheControlEphemeralTTLTTL1h
	return ctrl
}

// withLastToolCacheBreakpoint clones tools and marks the last custom tool so the
// entire tools array is part of a reusable cached prefix (tools render before system).
func withLastToolCacheBreakpoint(tools []sdk.ToolUnionParam) []sdk.ToolUnionParam {
	if len(tools) == 0 {
		return tools
	}
	out := append([]sdk.ToolUnionParam(nil), tools...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].OfTool == nil {
			continue
		}
		copied := *out[i].OfTool
		copied.CacheControl = ephemeralCacheControl()
		out[i].OfTool = &copied
		break
	}
	return out
}

func ToolToSDK(t ToolLike) sdk.ToolUnionParam {
	toolParam := sdk.ToolParam{
		Name:        t.Name(),
		Description: sdk.String(t.Description()),
		InputSchema: schemaToSDK(t.InputSchema()),
	}
	return sdk.ToolUnionParam{OfTool: &toolParam}
}

type ToolLike interface {
	Name() string
	Description() string
	InputSchema() map[string]any
}

func ToolsToSDK(tools []ToolLike) []sdk.ToolUnionParam {
	out := make([]sdk.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, ToolToSDK(t))
	}
	return out
}

func schemaToSDK(schema map[string]any) sdk.ToolInputSchemaParam {
	if schema == nil {
		return sdk.ToolInputSchemaParam{}
	}
	properties, _ := schema["properties"].(map[string]any)
	var required []string
	switch raw := schema["required"].(type) {
	case []string:
		required = raw
	case []any:
		for _, item := range raw {
			if s, ok := item.(string); ok {
				required = append(required, s)
			}
		}
	}
	param := sdk.ToolInputSchemaParam{
		Properties: properties,
		Required:   required,
	}
	for key, value := range schema {
		switch key {
		case "type", "properties", "required":
			continue
		default:
			if param.ExtraFields == nil {
				param.ExtraFields = make(map[string]any)
			}
			param.ExtraFields[key] = value
		}
	}
	return param
}

func TextFromMessage(message *sdk.Message) string {
	if message == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func ToolUseBlocks(message *sdk.Message) []sdk.ToolUseBlock {
	if message == nil {
		return nil
	}
	blocks := make([]sdk.ToolUseBlock, 0)
	for _, block := range message.Content {
		if block.Type == "tool_use" {
			blocks = append(blocks, block.AsToolUse())
		}
	}
	return blocks
}

func ToolResultMessage(toolUseID string, text string, isError bool) sdk.MessageParam {
	return sdk.NewUserMessage(ToolResultBlock(ToolResult{
		ToolUseID: toolUseID,
		Text:      text,
		IsError:   isError,
	}))
}

func ToolResultBlocks(results []ToolResult) []sdk.ContentBlockParamUnion {
	blocks := make([]sdk.ContentBlockParamUnion, 0, len(results))
	for _, result := range results {
		blocks = append(blocks, ToolResultBlock(result))
	}
	return blocks
}

// ToolResult is a tool invocation outcome sent back as a tool_result block.
// When ImageData is set, content includes a multimodal image block (plus Text).
type ToolResult struct {
	ToolUseID     string
	Text          string
	IsError       bool
	ImageMimeType string
	ImageData     string // base64-encoded image payload
}

// ToolResultBlock builds a single tool_result content block, optionally with an image.
func ToolResultBlock(result ToolResult) sdk.ContentBlockParamUnion {
	content := make([]sdk.ToolResultBlockParamContentUnion, 0, 2)
	if text := strings.TrimSpace(result.Text); text != "" || result.ImageData == "" {
		// Always include text when present, or a placeholder when there is no image.
		content = append(content, sdk.ToolResultBlockParamContentUnion{
			OfText: &sdk.TextBlockParam{Text: result.Text},
		})
	}
	if result.ImageData != "" {
		mimeType := result.ImageMimeType
		if mimeType == "" {
			mimeType = "image/png"
		}
		content = append(content, sdk.ToolResultBlockParamContentUnion{
			OfImage: &sdk.ImageBlockParam{
				Source: sdk.ImageBlockParamSourceUnion{
					OfBase64: &sdk.Base64ImageSourceParam{
						MediaType: sdk.Base64ImageSourceMediaType(mimeType),
						Data:      result.ImageData,
					},
				},
			},
		})
	}
	return sdk.ContentBlockParamUnion{
		OfToolResult: &sdk.ToolResultBlockParam{
			ToolUseID: result.ToolUseID,
			Content:   content,
			IsError:   sdk.Bool(result.IsError),
		},
	}
}

func RawInput(input json.RawMessage) json.RawMessage {
	if len(input) == 0 {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), input...)
}

func dispatchStreamCallbacks(req MessageRequest, event sdk.MessageStreamEventUnion) {
	switch ev := event.AsAny().(type) {
	case sdk.ContentBlockDeltaEvent:
		switch delta := ev.Delta.AsAny().(type) {
		case sdk.TextDelta:
			if req.OnTextDelta != nil {
				req.OnTextDelta(delta.Text)
			}
		case sdk.ThinkingDelta:
			if req.OnThinkingDelta != nil {
				req.OnThinkingDelta(delta.Thinking)
			}
		}
	}
}

func ValidateMessage(message *sdk.Message) error {
	if message == nil {
		return fmt.Errorf("message is nil")
	}
	if message.StopReason == sdk.StopReasonRefusal {
		return fmt.Errorf("model refused request")
	}
	return nil
}
