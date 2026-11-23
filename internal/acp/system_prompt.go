package acp

import (
	"fmt"
	"strings"

	"github.com/solosw/solcode/internal/config"
)

// SystemPrompt changes session-scoped core instructions. Tools, skill discovery,
// workspace rules and permission policy remain independent prompt layers.
type SystemPrompt struct {
	Mode    string         `json:"mode"`
	Content []ContentBlock `json:"content"`
}

type SystemPromptCapabilities struct {
	Mode SystemPromptModes `json:"mode"`
}

type SystemPromptModes struct {
	Override *struct{} `json:"override,omitempty"`
}

func validateSystemPrompt(prompt *SystemPrompt) error {
	if prompt == nil {
		return nil
	}
	if prompt.Mode != "append" && prompt.Mode != "override" {
		return fmt.Errorf("systemPrompt.mode must be append or override")
	}
	if len(prompt.Content) == 0 {
		return fmt.Errorf("systemPrompt.content must not be empty")
	}
	var nonEmpty bool
	for _, block := range prompt.Content {
		if block.Type != "text" {
			return fmt.Errorf("systemPrompt supports only text content blocks")
		}
		nonEmpty = nonEmpty || strings.TrimSpace(block.Text) != ""
	}
	if !nonEmpty {
		return fmt.Errorf("systemPrompt requires non-empty text")
	}
	return nil
}

func applySystemPrompt(cfg *config.Config, prompt *SystemPrompt) {
	// No prompt is persisted: clients re-send it on session/load.
	cfg.SystemPromptOverride, cfg.SystemPromptAppend = "", ""
	if prompt == nil {
		return
	}
	parts := make([]string, len(prompt.Content))
	for i, block := range prompt.Content {
		parts[i] = block.Text
	}
	text := strings.Join(parts, "\n\n")
	if prompt.Mode == "override" {
		cfg.SystemPromptOverride = text
	} else {
		cfg.SystemPromptAppend = text
	}
}
