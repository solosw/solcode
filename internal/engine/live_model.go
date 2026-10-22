package engine

import (
	"crypto/sha256"
	"encoding/json"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// UpdateMainModel changes only model-call settings, never hooks, permissions or
// callbacks. A call already in Create retains its snapshot. The main loop reads
// this state at the next API-call boundary; child runs retain their own snapshot.
func (e *Engine) UpdateMainModel(next Config) {
	if e.runOwner != nil {
		e = e.runOwner
	}
	e.configMu.Lock()
	defer e.configMu.Unlock()
	if e.config.ModelKey != next.ModelKey || e.config.ModelName != next.ModelName {
		e.resetReasoning = true
	}
	copyModelConfig(&e.config, next)
}

// InvalidateReasoningHistory is used when restoring a transcript whose opaque
// reasoning provenance cannot be verified (provider keys are not persisted).
// Visible messages and tool IDs remain intact at the next main-call boundary.
func (e *Engine) InvalidateReasoningHistory() {
	if e.runOwner != nil {
		e = e.runOwner
	}
	e.configMu.Lock()
	e.resetReasoning = true
	e.configMu.Unlock()
}

func copyModelConfig(dst *Config, src Config) {
	dst.Client, dst.ModelName, dst.ModelKey = src.Client, src.ModelName, src.ModelKey
	dst.FastModelName = src.FastModelName
	dst.MaxTokens, dst.MaxContextTokens = src.MaxTokens, src.MaxContextTokens
	dst.Thinking, dst.ThinkingText, dst.Effort = src.Thinking, src.ThinkingText, src.Effort
	dst.Stream = src.Stream
}

func (e *Engine) refreshMainModel(messages []sdk.MessageParam) []sdk.MessageParam {
	if e.runOwner == nil {
		return messages
	}
	owner := e.runOwner
	owner.configMu.Lock()
	defer owner.configMu.Unlock()
	copyModelConfig(&e.config, owner.config)
	reset := owner.resetReasoning
	owner.resetReasoning = false
	if !reset && len(owner.staleReasoning) == 0 {
		return messages
	}
	// Provider/model-specific signatures cannot cross model boundaries. Preserve
	// all visible answers, user messages, tool calls/results and their IDs.
	out := make([]sdk.MessageParam, 0, len(messages))
	for _, message := range messages {
		content := make([]sdk.ContentBlockParamUnion, 0, len(message.Content))
		for _, block := range message.Content {
			if block.OfThinking != nil || block.OfRedactedThinking != nil {
				encoded, _ := json.Marshal(block)
				fingerprint := sha256.Sum256(encoded)
				if reset {
					if owner.staleReasoning == nil {
						owner.staleReasoning = make(map[[32]byte]struct{})
					}
					owner.staleReasoning[fingerprint] = struct{}{}
					continue
				}
				if _, stale := owner.staleReasoning[fingerprint]; stale {
					continue
				}
			}
			content = append(content, block)
		}
		if len(content) > 0 {
			message.Content = content
			out = append(out, message)
		}
	}
	return out
}
