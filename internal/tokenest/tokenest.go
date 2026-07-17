package tokenest

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// Anthropic image token formula (docs):
// 1. Resize so the longest side ≤ MaxImageEdgePx while preserving aspect ratio.
// 2. tokens ≈ (width_px * height_px) / ImageTokenDivisor
// See: https://docs.anthropic.com/en/docs/build-with-claude/vision
const (
	// MaxImageEdgePx is the edge length Anthropic uses when normalizing images.
	MaxImageEdgePx = 1568
	// ImageTokenDivisor is used as tokens = (w * h) / ImageTokenDivisor.
	ImageTokenDivisor = 750
	// MinImageTokens is a small floor so empty/unknown images still cost something.
	MinImageTokens = 85
)

func Text(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return len([]rune(text))/4 + 1
}

// ImageTokens estimates Anthropic vision tokens for an image of the given
// pixel size. Dimensions are first normalized to MaxImageEdgePx like the API.
func ImageTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return MinImageTokens
	}
	w, h := normalizeImageSize(width, height)
	tokens := (w * h) / ImageTokenDivisor
	if tokens < MinImageTokens {
		return MinImageTokens
	}
	return tokens
}

// ImageTokensFromBytes estimates tokens when only the base64 payload size is
// known (e.g. no decoded dimensions). This is a coarse fallback and intentionally
// over-estimates slightly so the UI does not under-report context use.
func ImageTokensFromBytes(byteLen int) int {
	if byteLen <= 0 {
		return MinImageTokens
	}
	// Assume roughly JPEG ~1.5 bytes/pixel after API-side resize, then apply formula.
	// tokens ≈ pixels / 750 ≈ (bytes / 1.5) / 750 ≈ bytes / 1125
	tokens := byteLen / 1125
	if tokens < MinImageTokens {
		return MinImageTokens
	}
	// Cap at a full MaxImageEdgePx square to avoid absurd values from raw screenshots.
	maxTokens := ImageTokens(MaxImageEdgePx, MaxImageEdgePx)
	if tokens > maxTokens {
		return maxTokens
	}
	return tokens
}

func normalizeImageSize(width, height int) (int, int) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	maxEdge := width
	if height > maxEdge {
		maxEdge = height
	}
	if maxEdge <= MaxImageEdgePx {
		return width, height
	}
	scale := float64(MaxImageEdgePx) / float64(maxEdge)
	w := int(float64(width) * scale)
	h := int(float64(height) * scale)
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

func Messages(messages []sdk.MessageParam) int {
	// Text-only estimate + dedicated image tokens (images are not in Transcript).
	return Text(Transcript(messages)) + MessageImageTokens(messages)
}

// MessageImageTokens sums estimated vision tokens for all image blocks.
func MessageImageTokens(messages []sdk.MessageParam) int {
	total := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			total += contentBlockImageTokens(block)
			// Tool results can also carry images in some pipelines.
			if block.OfToolResult != nil {
				for _, content := range block.OfToolResult.Content {
					if content.OfImage != nil {
						total += imageBlockTokens(content.OfImage)
					}
				}
			}
		}
	}
	return total
}

func contentBlockImageTokens(block sdk.ContentBlockParamUnion) int {
	if block.OfImage != nil {
		return imageBlockTokens(block.OfImage)
	}
	return 0
}

func imageBlockTokens(block *sdk.ImageBlockParam) int {
	if block == nil {
		return 0
	}
	if block.Source.OfBase64 != nil {
		// base64 length ≈ 4/3 of raw bytes
		rawApprox := len(block.Source.OfBase64.Data) * 3 / 4
		return ImageTokensFromBytes(rawApprox)
	}
	if block.Source.OfURL != nil {
		// URL images: dimensions unknown; use a mid-size default.
		return ImageTokens(1024, 1024)
	}
	return MinImageTokens
}

func Request(system string, messages []sdk.MessageParam, tools []sdk.ToolUnionParam) int {
	approx := Text(system)
	approx += Messages(messages)
	approx += Tools(tools)
	return approx
}

func Tools(tools []sdk.ToolUnionParam) int {
	approx := 0
	for _, toolDef := range tools {
		if toolDef.OfTool == nil {
			continue
		}
		approx += Text(toolDef.OfTool.Name)
		description := strings.TrimSpace(toolDef.OfTool.Description.Value)
		if description != "" {
			approx += Text(description)
		}
		approx += Text(fmt.Sprintf("%v", toolDef.OfTool.InputSchema))
	}
	return approx
}

func Transcript(messages []sdk.MessageParam) string {
	var b strings.Builder
	for _, msg := range messages {
		role := string(msg.Role)
		for _, block := range msg.Content {
			text := contentBlockText(block)
			if strings.TrimSpace(text) == "" {
				// Still mark image presence in transcript for debugging/display.
				if block.OfImage != nil {
					if role != "" {
						b.WriteString(role)
						b.WriteString(": ")
					}
					b.WriteString(fmt.Sprintf("[image ~%d tokens]", contentBlockImageTokens(block)))
					b.WriteString("\n")
				}
				continue
			}
			if role != "" {
				b.WriteString(role)
				b.WriteString(": ")
			}
			b.WriteString(strings.TrimSpace(text))
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func contentBlockText(block sdk.ContentBlockParamUnion) string {
	if block.OfText != nil {
		return block.OfText.Text
	}
	if block.OfToolResult != nil {
		return toolResultText(block.OfToolResult)
	}
	if block.OfToolUse != nil {
		return toolUseText(block.OfToolUse)
	}
	return ""
}

func toolUseText(block *sdk.ToolUseBlockParam) string {
	if block == nil {
		return ""
	}
	input := strings.TrimSpace(formatJSON(block.Input))
	if input == "" {
		return "[tool use: " + block.Name + "]"
	}
	return "[tool use: " + block.Name + "]\n" + input
}

func toolResultText(block *sdk.ToolResultBlockParam) string {
	if block == nil {
		return ""
	}
	parts := make([]string, 0, len(block.Content))
	for _, content := range block.Content {
		if content.OfText != nil {
			parts = append(parts, content.OfText.Text)
			continue
		}
		if text := content.GetText(); text != nil {
			parts = append(parts, *text)
			continue
		}
		if content.OfImage != nil {
			parts = append(parts, fmt.Sprintf("[image ~%d tokens]", imageBlockTokens(content.OfImage)))
			continue
		}
		if raw := strings.TrimSpace(formatJSON(content)); raw != "" {
			parts = append(parts, raw)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return "[tool result]"
	}
	return "[tool result]\n" + text
}

func formatJSON(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}
