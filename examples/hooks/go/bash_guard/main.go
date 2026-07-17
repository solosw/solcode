// PreToolUse Bash guard (Go).
//
// settings command (from repo root):
//
//	go run ./examples/hooks/go/bash_guard
//
// Prefer a built binary for lower latency:
//
//	go build -o bin/bash-guard ./examples/hooks/go/bash_guard
//	// command: bin/bash-guard   (or bash-guard.exe on Windows)
package main

import (
	"encoding/json"
	"io"
	"os"
	"regexp"
)

type event struct {
	ToolInput map[string]any `json:"tool_input"`
}

type result struct {
	Decision string `json:"decision"`
	Message  string `json:"message,omitempty"`
}

var deny = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?/\b`),
	regexp.MustCompile(`(?i)\bformat\s+[a-z]:`),
	regexp.MustCompile(`(?i)\b(curl|wget|Invoke-WebRequest)\b`),
	regexp.MustCompile(`(?i)\bgit\s+push\s+.*--force\b`),
	regexp.MustCompile(`(?i)\bdrop\s+database\b`),
}

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil || len(raw) == 0 {
		write(result{Decision: "allow"})
		return
	}
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil {
		write(result{Decision: "allow", Message: "invalid event json"})
		return
	}
	cmd, _ := ev.ToolInput["command"].(string)
	for _, re := range deny {
		if re.MatchString(cmd) {
			write(result{
				Decision: "block",
				Message:  "Bash command blocked by go bash_guard: " + cmd,
			})
			return
		}
	}
	write(result{Decision: "allow"})
}

func write(r result) {
	_ = json.NewEncoder(os.Stdout).Encode(r)
}
