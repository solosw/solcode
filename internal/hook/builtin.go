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
	// Aggressiveness is headroom strength 0..1 (default 0.5).
	Aggressiveness float64
	// SkipTools are tool names that must keep full results.
	// Empty by default: Edit/Write/Diff and MCP tools are compressed too.
	// View uses a separate structure-preserving path (not headroom fold).
	SkipTools []string
}

func defaultCompressOptions() CompressToolResultOptions {
	return CompressToolResultOptions{
		Aggressiveness: 0.5,
		SkipTools:      nil,
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
	// BatchTool owns a structured, budgeted envelope. Its children must reach
	// that budgeter without builtin lossy folding, so archived outputs remain
	// recoverable. This does not bypass user-defined post-tool hooks.
	if event.OutputManaged || strings.TrimSpace(event.ToolName) == tool.BatchToolName {
		return Result{Decision: DecisionAllow}, nil
	}
	// Never rewrite errors, multimodal images, or empty text.
	if block.IsError || block.Type == "image" || strings.TrimSpace(block.Text) == "" {
		return Result{Decision: DecisionAllow}, nil
	}
	if shouldSkipCompressTool(event.ToolName, opts.SkipTools) {
		return Result{Decision: DecisionAllow}, nil
	}

	origTokens := tokenest.Text(block.Text)
	var (
		compressed string
		err        error
	)
	if isStructurePreservingCompressTool(event.ToolName) {
		// View (and similar read tools): never fold away middle lines with
		// headroom's "[...N more lines...]". Only trim pathological line length
		// and collapse runs of blank lines so the model still sees the code.
		compressed = compressViewStructuredText(block.Text)
	} else {
		compressed, err = compressTextLegacy(block.Text, opts.Aggressiveness)
		if err != nil {
			return Result{Decision: DecisionAllow}, err
		}
		compressed = strings.TrimSpace(compressed)
	}
	if compressed == "" {
		return Result{Decision: DecisionAllow}, nil
	}
	if compressed == block.Text {
		return Result{Decision: DecisionAllow}, nil
	}

	out := *block
	if out.Type == "" {
		out.Type = "text"
	}
	// Do not inject a human-visible banner into tool_result text; token delta is
	// recorded only on the hook message for diagnostics.
	out.Text = compressed
	compTokens := tokenest.Text(compressed)
	return Result{
		Decision:       DecisionModify,
		ModifiedResult: &out,
		Message:        fmt.Sprintf("compressed %s tool result ~%d→%d tokens", event.ToolName, origTokens, compTokens),
	}, nil
}

// viewMaxCompressLine caps single-line bloat in View-shaped tool results.
// Matches tool.MaxLineLength so PostToolUse does not invent a second policy.
const viewMaxCompressLine = 2000

func isStructurePreservingCompressTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "View":
		return true
	default:
		return false
	}
}

// compressViewStructuredText keeps every source line visible. It only:
//   - truncates individual lines longer than viewMaxCompressLine
//   - collapses consecutive blank lines (including View "N|" empty rows) to one
//
// It must never emit headroom-style structural elision such as
// "[...N more lines...]".
func compressViewStructuredText(text string) string {
	if text == "" {
		return text
	}
	// Preserve whether the original ended with a trailing newline.
	endsWithNL := strings.HasSuffix(text, "\n")
	// Normalize CRLF so blank-line detection is stable; View emits "\n".
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	rawLines := strings.Split(text, "\n")
	// Split keeps a trailing empty element when text ends with "\n"; drop it and
	// re-apply endsWithNL at the end so we don't invent an extra blank line.
	if endsWithNL && len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}

	out := make([]string, 0, len(rawLines))
	prevBlank := false
	for _, line := range rawLines {
		line = truncateViewCompressLine(line)
		blank := isViewCompressBlankLine(line)
		if blank {
			if prevBlank {
				continue
			}
			prevBlank = true
		} else {
			prevBlank = false
		}
		out = append(out, line)
	}
	result := strings.Join(out, "\n")
	if endsWithNL {
		result += "\n"
	}
	return result
}

func truncateViewCompressLine(line string) string {
	// Prefer truncating the content after a View line-number prefix so the
	// "N|" marker stays intact for the model.
	if prefix, content, ok := splitViewNumberedLine(line); ok {
		if len(content) <= viewMaxCompressLine {
			return line
		}
		return prefix + content[:viewMaxCompressLine] + "... [truncated]"
	}
	if len(line) <= viewMaxCompressLine {
		return line
	}
	return line[:viewMaxCompressLine] + "... [truncated]"
}

// isViewCompressBlankLine reports lines that carry no source text. Plain empty
// rows and View-numbered empty rows ("     12|") both count so blank runs in
// real View output can collapse.
func isViewCompressBlankLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	if _, content, ok := splitViewNumberedLine(line); ok {
		return strings.TrimSpace(content) == ""
	}
	return false
}

// splitViewNumberedLine parses "   12|rest" View rows. ok is false when the
// line is not in that shape (e.g. "<file>" wrappers).
func splitViewNumberedLine(line string) (prefix, content string, ok bool) {
	// Match optional leading spaces, digits, then '|'.
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	j := i
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	if j == i || j >= len(line) || line[j] != '|' {
		return "", "", false
	}
	return line[:j+1], line[j+1:], true
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
