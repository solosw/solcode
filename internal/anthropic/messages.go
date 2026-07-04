package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

type MessageRequest struct {
	Model           string
	MaxTokens       int64
	System          string
	Messages        []sdk.MessageParam
	Tools           []sdk.ToolUnionParam
	Thinking        bool
	ThinkingText    bool
	Effort          string
	Stream          bool
	OnTextDelta     func(string)
	OnThinkingDelta func(string)
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
	params := sdk.MessageNewParams{
		Model:     sdk.Model(model),
		MaxTokens: maxTokens,
		Messages:  r.Messages,
		Tools:     r.Tools,
	}
	if r.System != "" {
		params.System = []sdk.TextBlockParam{{Text: r.System}}
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
	return sdk.NewUserMessage(sdk.NewToolResultBlock(toolUseID, text, isError))
}

func ToolResultBlocks(results []ToolResult) []sdk.ContentBlockParamUnion {
	blocks := make([]sdk.ContentBlockParamUnion, 0, len(results))
	for _, result := range results {
		blocks = append(blocks, sdk.NewToolResultBlock(result.ToolUseID, result.Text, result.IsError))
	}
	return blocks
}

type ToolResult struct {
	ToolUseID string
	Text      string
	IsError   bool
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
