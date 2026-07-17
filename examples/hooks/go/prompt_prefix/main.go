// UserPromptSubmit prompt prefix (Go).
//
//	go run ./examples/hooks/go/prompt_prefix
package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
)

type event struct {
	Prompt string `json:"prompt"`
}

type result struct {
	Decision       string `json:"decision"`
	ModifiedPrompt string `json:"modified_prompt,omitempty"`
	Message        string `json:"message,omitempty"`
}

func main() {
	prefix := os.Getenv("SOLCODE_PROMPT_PREFIX")
	if prefix == "" {
		prefix = "[project note: prefer small diffs; run tests after edits]\n\n"
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil || len(raw) == 0 {
		write(result{Decision: "allow"})
		return
	}
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil {
		write(result{Decision: "allow"})
		return
	}
	if ev.Prompt == "" || strings.HasPrefix(ev.Prompt, strings.TrimSpace(prefix)) {
		write(result{Decision: "allow"})
		return
	}
	write(result{
		Decision:       "modify",
		ModifiedPrompt: prefix + ev.Prompt,
		Message:        "prefixed user prompt",
	})
}

func write(r result) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(r)
}
