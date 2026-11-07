package organizer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Summarize uses the same configured generator and limits as archival extraction.
// Unlike Organize, it never silently truncates the transcript to fit a small GGUF.
func (o *Organizer) Summarize(ctx context.Context, system, previous, transcript string, contextSize int) (string, error) {
	if !o.Available() {
		return "", ErrUnavailable
	}
	if contextSize <= 0 {
		contextSize = 16384
	}
	system = fmt.Sprintf("%s\n\nMaximum summary output: %d tokens. Finish all required sections within this limit.", system, o.maxOutputTokens)
	budget := contextSize - len(system) - o.maxOutputTokens - 512
	if budget < 512 {
		return "", fmt.Errorf("organizer context is too small for the summary prompt and output budget")
	}
	text := transcript
	if strings.TrimSpace(previous) != "" {
		text = "Previous summary:\n" + previous + "\n\nNew transcript:\n" + transcript
	}
	// Count bytes conservatively, including JSON escaping, rather than guessing
	// that an API model accepts a whole main-agent history in its smaller window.
	for pass := 0; pass < 12; pass++ {
		originalBytes := len(text)
		parts := []string{}
		for len(text) > 0 {
			end := len(text)
			if end > budget/2 {
				end = budget / 2
			}
			for end > 0 && !utf8.ValidString(text[:end]) {
				end--
			}
			if end == 0 {
				return "", fmt.Errorf("cannot partition summary input")
			}
			payload, _ := json.Marshal(map[string]string{"previous_summary": "", "transcript": text[:end]})
			if len(payload) > budget {
				return "", fmt.Errorf("summary input exceeds configured context budget")
			}
			runCtx := ctx
			cancel := func() {}
			if o.timeout > 0 {
				runCtx, cancel = context.WithTimeout(ctx, o.timeout)
			}
			result, err := o.gen.Generate(runCtx, GenerateRequest{RequireComplete: true, System: system, User: string(payload), MaxTokens: o.maxOutputTokens, Temperature: o.temperature})
			cancel()
			if err != nil {
				return "", fmt.Errorf("organizer summary: %w", err)
			}
			result = strings.TrimSpace(result)
			if result == "" {
				return "", fmt.Errorf("organizer summary: empty response")
			}
			parts = append(parts, result)
			text = text[end:]
		}
		if len(parts) == 1 {
			return parts[0], nil
		}
		merged := strings.Join(parts, "\n\n")
		// Bound recursive summarization; non-reducing generations must fail safely.
		if len(merged) >= originalBytes {
			return "", fmt.Errorf("summary failed to reduce input")
		}
		text = merged
	}
	return "", fmt.Errorf("summary exceeded hierarchical generation limit")
}
