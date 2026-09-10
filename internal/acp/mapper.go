package acp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

func promptToText(blocks []ContentBlock, workDir string) string {
	var parts []string
	for _, block := range blocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "", "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		case "image":
			path, err := writePromptImage(block, workDir)
			if err != nil {
				parts = append(parts, fmt.Sprintf("[failed to attach image: %v]", err))
				continue
			}
			parts = append(parts, "@"+quotePath(path))
		case "resource_link":
			label := strings.TrimSpace(block.Name)
			uri := strings.TrimSpace(block.URI)
			switch {
			case label != "" && uri != "":
				parts = append(parts, fmt.Sprintf("%s (%s)", label, uri))
			case uri != "":
				parts = append(parts, uri)
			case label != "":
				parts = append(parts, label)
			}
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		case "resource":
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
				continue
			}
			uri := resourceURI(block)
			if uri != "" {
				parts = append(parts, uri)
			}
			if name := strings.TrimSpace(block.Name); name != "" && name != uri {
				parts = append(parts, name)
			}
		default:
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func writePromptImage(block ContentBlock, workDir string) (string, error) {
	data := strings.TrimSpace(block.Data)
	if data == "" {
		return "", fmt.Errorf("image data is empty")
	}
	mime := strings.ToLower(strings.TrimSpace(block.MimeType))
	ext := ".png"
	switch mime {
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	case "image/png", "":
		ext = ".png"
	default:
		if strings.HasPrefix(mime, "image/") {
			ext = "." + strings.TrimPrefix(mime, "image/")
		}
	}
	dir := filepath.Join(workDir, ".solcode", "acp-uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := strings.TrimSpace(block.Name)
	if name == "" {
		name = "image" + ext
	} else if filepath.Ext(name) == "" {
		name += ext
	}
	path := filepath.Join(dir, sanitizeFileName(name))
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, decoded, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func quotePath(path string) string {
	if strings.ContainsAny(path, " \t") {
		return `"` + path + `"`
	}
	return path
}

func sanitizeFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "image.png"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "image.png"
	}
	return out
}

func resourceURI(block ContentBlock) string {
	if uri := strings.TrimSpace(block.URI); uri != "" {
		return uri
	}
	if len(block.Resource) == 0 {
		return ""
	}
	var payload struct {
		URI  string `json:"uri"`
		Text string `json:"text"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(block.Resource, &payload); err != nil {
		return ""
	}
	if payload.Text != "" {
		return payload.Text
	}
	if payload.URI != "" {
		return payload.URI
	}
	return payload.Name
}

func sessionHistoryUpdates(s *session.Session) []SessionUpdate {
	if s == nil {
		return nil
	}
	var updates []SessionUpdate
	for _, msg := range s.Messages {
		role := strings.ToLower(string(msg.Role))
		for _, block := range msg.Content {
			switch {
			case block.OfText != nil:
				text := strings.TrimSpace(block.OfText.Text)
				if text == "" {
					continue
				}
				kind := "agent_message_chunk"
				if role == "user" {
					kind = "user_message_chunk"
				}
				content := ContentBlock{Type: "text", Text: text}
				updates = append(updates, SessionUpdate{SessionUpdate: kind, Content: &content})
			case block.OfThinking != nil:
				// Match claude-agent-acp: thinking → agent_thought_chunk with content{type:text}.
				text := strings.TrimSpace(block.OfThinking.Thinking)
				if text == "" {
					continue
				}
				content := ContentBlock{Type: "text", Text: text}
				updates = append(updates, SessionUpdate{SessionUpdate: "agent_thought_chunk", Content: &content})
			case block.OfToolUse != nil:
				input, _ := json.Marshal(block.OfToolUse.Input)
				updates = append(updates, SessionUpdate{
					SessionUpdate: "tool_call",
					ToolCallID:    block.OfToolUse.ID,
					Title:         block.OfToolUse.Name,
					Kind:          toolKind(block.OfToolUse.Name),
					Status:        ToolCallCompleted,
					RawInput:      input,
				})
			case block.OfToolResult != nil:
				text := toolResultText(block.OfToolResult)
				output, _ := json.Marshal(map[string]string{"output": text})
				status := ToolCallCompleted
				if toolResultIsError(block.OfToolResult) {
					status = ToolCallFailed
				}
				toolContent, locations := toolOutputContent("", text, status == ToolCallFailed)
				updates = append(updates, SessionUpdate{
					SessionUpdate: "tool_call_update",
					ToolCallID:    block.OfToolResult.ToolUseID,
					Status:        status,
					RawOutput:     output,
					ToolContent:   toolContent,
					Locations:     locations,
				})
			}
		}
	}
	return updates
}

func toolResultText(result *sdk.ToolResultBlockParam) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, content := range result.Content {
		if content.OfText != nil {
			parts = append(parts, content.OfText.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func toolResultIsError(result *sdk.ToolResultBlockParam) bool {
	if result == nil {
		return false
	}
	return result.IsError.Or(false)
}

func toolKind(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "view", "glob", "grep", "ls", "viewimage":
		return "read"
	case "edit", "multiedit", "write", "multiwrite", "patch":
		return "edit"
	case "bash":
		return "execute"
	case "websearch", "fetch":
		return "fetch"
	case "task", "subagent":
		return "think"
	case "imagegenerate", "imageedit":
		return "other"
	default:
		return "other"
	}
}

// toolCallDiffs builds ACP diff content + locations from file-mutating tool input.
// Best-effort preview for client display; execution still happens in the tools.
func toolCallDiffs(name string, input json.RawMessage, workDir string) (content []ToolCallContent, locations []ToolCallLocation) {
	uctx := &tool.UseContext{WorkDir: strings.TrimSpace(workDir)}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case strings.ToLower(tool.EditToolName):
		var params tool.EditParams
		if err := json.Unmarshal(input, &params); err != nil || strings.TrimSpace(params.Path) == "" {
			return nil, nil
		}
		path := tool.ResolvePath(uctx, params.Path)
		oldText, existed, err := readFileText(path)
		if err != nil {
			return nil, toolLocations(path)
		}
		newText, ok := previewEdit(oldText, existed, params)
		if !ok {
			return nil, toolLocations(path)
		}
		return []ToolCallContent{diffContent(path, oldText, newText, existed)}, toolLocations(path)

	case strings.ToLower(tool.WriteToolName):
		var params tool.WriteParams
		if err := json.Unmarshal(input, &params); err != nil || strings.TrimSpace(params.Path) == "" {
			return nil, nil
		}
		path := tool.ResolvePath(uctx, params.Path)
		oldText, existed, err := readFileText(path)
		if err != nil {
			return nil, toolLocations(path)
		}
		return []ToolCallContent{diffContent(path, oldText, params.Content, existed)}, toolLocations(path)

	case strings.ToLower(tool.MultiWriteToolName):
		var params tool.MultiWriteParams
		if err := json.Unmarshal(input, &params); err != nil || len(params.Files) == 0 {
			return nil, nil
		}
		for _, file := range params.Files {
			if strings.TrimSpace(file.Path) == "" {
				continue
			}
			path := tool.ResolvePath(uctx, file.Path)
			oldText, existed, err := readFileText(path)
			if err != nil {
				locations = append(locations, ToolCallLocation{Path: path})
				continue
			}
			content = append(content, diffContent(path, oldText, file.Content, existed))
			locations = append(locations, ToolCallLocation{Path: path})
		}
		return content, locations

	case strings.ToLower(tool.MultiEditToolName):
		var params tool.MultiEditParams
		if err := json.Unmarshal(input, &params); err != nil || len(params.Edits) == 0 {
			return nil, nil
		}
		type staged struct {
			path    string
			before  string
			after   string
			existed bool
		}
		order := make([]string, 0)
		byPath := make(map[string]*staged)
		for _, edit := range params.Edits {
			if strings.TrimSpace(edit.Path) == "" {
				continue
			}
			path := tool.ResolvePath(uctx, edit.Path)
			file := byPath[path]
			if file == nil {
				oldText, existed, err := readFileText(path)
				if err != nil {
					continue
				}
				file = &staged{path: path, before: oldText, after: oldText, existed: existed}
				byPath[path] = file
				order = append(order, path)
			}
			next, ok := previewEdit(file.after, file.existed || file.after != "", edit)
			if !ok {
				// Keep path for follow-along even if preview fails.
				continue
			}
			file.after = next
			if edit.OldString == "" {
				file.existed = false
			}
		}
		for _, path := range order {
			file := byPath[path]
			if file == nil || file.before == file.after {
				locations = append(locations, ToolCallLocation{Path: path})
				continue
			}
			content = append(content, diffContent(file.path, file.before, file.after, file.existed && file.before != ""))
			locations = append(locations, ToolCallLocation{Path: path})
		}
		return content, locations

	case strings.ToLower(tool.PatchToolName):
		var params tool.PatchParams
		if err := json.Unmarshal(input, &params); err != nil || strings.TrimSpace(params.Path) == "" {
			return nil, nil
		}
		path := tool.ResolvePath(uctx, params.Path)
		// Patch apply lives inside the tool; expose path + patch text for UI.
		return []ToolCallContent{{
			Type: "content",
			Content: &ContentBlock{
				Type: "text",
				Text: fmt.Sprintf("Patch %s\n\n%s", path, params.PatchText),
			},
		}}, toolLocations(path)

	default:
		return nil, nil
	}
}

func readFileText(path string) (text string, existed bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

func previewEdit(current string, existed bool, params tool.EditParams) (string, bool) {
	// Create file (or overwrite empty create path used by Edit).
	if params.OldString == "" {
		if existed && current != "" {
			return "", false
		}
		return params.NewString, true
	}
	if !existed && current == "" {
		return "", false
	}
	// Delete content
	if params.NewString == "" {
		index := strings.Index(current, params.OldString)
		if index < 0 {
			return "", false
		}
		if strings.LastIndex(current, params.OldString) != index {
			return "", false
		}
		return current[:index] + current[index+len(params.OldString):], true
	}
	// Replace content
	index := strings.Index(current, params.OldString)
	if index < 0 {
		return "", false
	}
	if strings.LastIndex(current, params.OldString) != index {
		return "", false
	}
	return current[:index] + params.NewString + current[index+len(params.OldString):], true
}

func diffContent(path, oldText, newText string, existed bool) ToolCallContent {
	c := ToolCallContent{
		Type:    "diff",
		Path:    path,
		NewText: newText,
	}
	if existed {
		old := oldText
		c.OldText = &old
	}
	return c
}

func toolLocations(paths ...string) []ToolCallLocation {
	out := make([]ToolCallLocation, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		out = append(out, ToolCallLocation{Path: path})
	}
	return out
}

func usageUpdate(usage engine.Usage) *UsageUpdate {
	// Match the TUI footer "ctx used/limit":
	//   used  = EstimatedContextTokens (ContextBuilder occupancy)
	//   size  = MaxContextTokens (cfg.MaxContextTokens)
	// Do not fall back to cumulative API billing tokens — those are session
	// spend totals, not current window occupancy, and diverge from the TUI.
	return &UsageUpdate{
		Used: usage.EstimatedContextTokens,
		Size: usage.MaxContextTokens,
	}
}

func availableCommands() []AvailableCommand {
	return []AvailableCommand{
		{Name: "help", Description: "Show available commands"},
		{Name: "status", Description: "Show model, context, and cache usage"},
		{Name: "model", Description: "Select a model from the current provider"},
		{Name: "provider", Description: "Select a provider"},
		{Name: "effort", Description: "Select thinking effort"},
		{Name: "sessions", Description: "List or switch saved sessions"},
		{Name: "compact", Description: "Compact the current session now"},
		{Name: "fix-session", Description: "Repair invalid tool-use chains in the current session"},
		{Name: "new-session", Description: "Create and switch to a new session", Input: &AvailableCommandInput{Hint: "optional name"}},
		{Name: "skills", Description: "Browse skills and toggle enabled/disabled"},
		{Name: "mcp", Description: "Browse MCP servers and toggle enabled/disabled"},
		{Name: "proxy", Description: "Show or set HTTP(S) proxy", Input: &AvailableCommandInput{Hint: "url | on | off | clear"}},
		{Name: "goal", Description: "Work from goal.md until complete", Input: &AvailableCommandInput{Hint: "optional description"}},
		{Name: "workflows", Description: "List loaded workflows"},
	}
}
