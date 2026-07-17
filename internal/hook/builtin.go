package hook

import (
	"fmt"
	"strings"

	headroom "github.com/superops-team/headroom-go"

	"github.com/solosw/solcode/internal/tokenest"
	"github.com/solosw/solcode/internal/tool"
)

// Builtin hook names.
const (
	BuiltinCompressToolResult        = "compress_tool_result"
	BuiltinDisableCompressToolResult = "disable_compress_tool_result"
)

// CompressToolResultOptions controls the PostToolUse headroom compressor.
type CompressToolResultOptions struct {
	// MinTokens skips compression for smaller payloads (default 800).
	MinTokens int
	// Aggressiveness is headroom strength 0..1 (default 0.5).
	Aggressiveness float64
	// SkipTools are tool names that must keep full results (Edit/Write/…).
	SkipTools []string
}

func defaultCompressOptions() CompressToolResultOptions {
	return CompressToolResultOptions{
		MinTokens:      800,
		Aggressiveness: 0.5,
		SkipTools:      []string{"Edit", "Write", "Patch", "Diff"},
	}
}

// DefaultConfig enables the built-in PostToolUse tool-result compressor.
// User settings can replace or extend hooks; fail_mode is open so compression
// errors never block tool delivery.
func DefaultConfig() Config {
	return Config{
		Events: map[EventName][]MatcherConfig{
			EventPostToolUse: {
				{
					Matcher: "*",
					Hooks: []CommandConfig{
						{
							Type:     "builtin",
							Name:     BuiltinCompressToolResult,
							FailMode: "open",
						},
					},
				},
			},
		},
	}
}

func runBuiltinHook(cfg CommandConfig, event Event) (Result, error) {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		// Allow type "builtin" with command field as alias for name.
		name = strings.TrimSpace(cfg.Command)
	}
	switch name {
	case BuiltinCompressToolResult:
		return compressToolResultHook(event, defaultCompressOptions())
	case BuiltinDisableCompressToolResult:
		// No-op marker used by config to opt out of default compression.
		return Result{Decision: DecisionAllow}, nil
	case "":
		return Result{Decision: DecisionAllow}, fmt.Errorf("builtin hook name is required")
	default:
		return Result{Decision: DecisionAllow}, fmt.Errorf("unknown builtin hook: %s", name)
	}
}

func compressToolResultHook(event Event, opts CompressToolResultOptions) (Result, error) {
	if event.Name != EventPostToolUse {
		return Result{Decision: DecisionAllow}, nil
	}
	block := contentBlockFromAny(event.ToolResult)
	if block == nil {
		return Result{Decision: DecisionAllow}, nil
	}
	// Never rewrite errors, multimodal images, or empty text.
	if block.IsError || block.Type == "image" || strings.TrimSpace(block.Text) == "" {
		return Result{Decision: DecisionAllow}, nil
	}
	if shouldSkipCompressTool(event.ToolName, opts.SkipTools) {
		return Result{Decision: DecisionAllow}, nil
	}

	minTokens := opts.MinTokens
	if minTokens <= 0 {
		minTokens = 800
	}
	origTokens := tokenest.Text(block.Text)
	if origTokens < minTokens {
		return Result{Decision: DecisionAllow}, nil
	}

	compressed, err := compressTextLegacy(block.Text, opts.Aggressiveness)
	if err != nil {
		return Result{Decision: DecisionAllow}, err
	}
	compressed = strings.TrimSpace(compressed)
	if compressed == "" {
		return Result{Decision: DecisionAllow}, nil
	}
	compTokens := tokenest.Text(compressed)
	// Only apply when we actually save a meaningful amount (≥15% and ≥100 tokens).
	if compTokens >= origTokens || origTokens-compTokens < 100 {
		return Result{Decision: DecisionAllow}, nil
	}
	savedPct := float64(origTokens-compTokens) / float64(origTokens)
	if savedPct < 0.15 {
		return Result{Decision: DecisionAllow}, nil
	}

	out := *block
	if out.Type == "" {
		out.Type = "text"
	}
	// Do not inject a human-visible banner into tool_result text; savings are
	// recorded only on the hook message for diagnostics.
	out.Text = compressed
	return Result{
		Decision:       DecisionModify,
		ModifiedResult: &out,
		Message:        fmt.Sprintf("compressed %s tool result ~%d→%d tokens", event.ToolName, origTokens, compTokens),
	}, nil
}

func compressTextLegacy(text string, aggressiveness float64) (string, error) {
	opts := headroom.DefaultOptions()
	if aggressiveness <= 0 {
		aggressiveness = 0.5
	}
	if aggressiveness > 1 {
		aggressiveness = 1
	}
	opts.Aggressiveness = aggressiveness
	opts.Reversible = false
	opts.EnablePipeline = false // probe: legacy saves ~70-85% on real tool dumps; pipeline ~0%
	result, err := headroom.Compress([]headroom.Message{{Role: "tool", Content: text}}, opts)
	if err != nil {
		return "", err
	}
	if result == nil || len(result.Messages) == 0 {
		return text, nil
	}
	return result.Messages[0].Content, nil
}

func shouldSkipCompressTool(name string, skip []string) bool {
	name = strings.TrimSpace(name)
	for _, s := range skip {
		if strings.EqualFold(strings.TrimSpace(s), name) {
			return true
		}
	}
	return false
}

func contentBlockFromAny(v any) *tool.ContentBlock {
	switch t := v.(type) {
	case *tool.ContentBlock:
		return t
	case tool.ContentBlock:
		c := t
		return &c
	case map[string]any:
		// JSON-decoded tool_result from command hooks / re-entry.
		block := &tool.ContentBlock{Type: "text"}
		if s, ok := t["type"].(string); ok {
			block.Type = s
		}
		if s, ok := t["text"].(string); ok {
			block.Text = s
		}
		if b, ok := t["is_error"].(bool); ok {
			block.IsError = b
		}
		if s, ok := t["mime_type"].(string); ok {
			block.MimeType = s
		}
		if s, ok := t["data"].(string); ok {
			block.Data = s
		}
		return block
	default:
		return nil
	}
}

// ApplyModifiedResult merges a hook ModifiedResult into the current content block.
func ApplyModifiedResult(current *tool.ContentBlock, modified any) *tool.ContentBlock {
	if modified == nil {
		return current
	}
	if block := contentBlockFromAny(modified); block != nil {
		// If only text was provided in a sparse map, keep other fields from current.
		if current != nil {
			if block.Type == "" {
				block.Type = current.Type
			}
			if block.Type == "text" && block.Text == "" && current.Text != "" && modifiedMapOnlyEmptyText(modified) {
				return current
			}
			if block.MimeType == "" {
				block.MimeType = current.MimeType
			}
			if block.Data == "" {
				block.Data = current.Data
			}
			if block.ToolUseID == "" {
				block.ToolUseID = current.ToolUseID
			}
		}
		return block
	}
	return current
}

func modifiedMapOnlyEmptyText(modified any) bool {
	m, ok := modified.(map[string]any)
	if !ok {
		return false
	}
	_, hasText := m["text"]
	return hasText && strings.TrimSpace(fmt.Sprint(m["text"])) == ""
}
