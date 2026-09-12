package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/solosw/solcode/internal/checkpoint"
)

// FormatCheckpointsList returns the /checkpoints command output.
func FormatCheckpointsList(application *App, sessionID, workDir string) string {
	if application == nil {
		return "Application is not available."
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "main"
	}
	metas, err := application.ListCheckpoints(sessionID, workDir)
	if err != nil {
		return fmt.Sprintf("Could not list checkpoints: %v", err)
	}
	if len(metas) == 0 {
		return "No checkpoints yet. Edit files with Edit/Write/Patch during a turn, then /rewind <turn|name>."
	}
	lines := []string{
		"Code checkpoints (conversation is not restored):",
		"Restore: /rewind <turn|name>",
		"Name:    /checkpoint-name <name> [turn]",
		"",
	}
	for i := len(metas) - 1; i >= 0; i-- {
		meta := metas[i]
		prompt := strings.TrimSpace(meta.Prompt)
		if prompt == "" {
			prompt = "(empty prompt)"
		}
		if runes := []rune(prompt); len(runes) > 72 {
			prompt = string(runes[:72]) + "…"
		}
		when := meta.Time.Local().Format("2006-01-02 15:04:05")
		label := ""
		if name := strings.TrimSpace(meta.Name); name != "" {
			label = "  [" + name + "]"
		}
		lines = append(lines, fmt.Sprintf("%d  %s  files=%d%s  %s", meta.Turn, when, meta.FileCount, label, prompt))
	}
	return strings.Join(lines, "\n")
}

// FormatCheckpointNameCommand handles /checkpoint-name <name> [turn].
func FormatCheckpointNameCommand(application *App, sessionID, workDir, args string) string {
	if application == nil {
		return "Application is not available."
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "main"
	}
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) < 1 || len(fields) > 2 {
		return "Usage: /checkpoint-name <name> [turn]"
	}
	turn := -1
	if len(fields) == 2 {
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 0 {
			return "Usage: /checkpoint-name <name> [turn]"
		}
		turn = n
	}
	meta, err := application.NameCheckpoint(sessionID, workDir, turn, fields[0])
	if err != nil {
		return fmt.Sprintf("Could not name checkpoint: %v", err)
	}
	return fmt.Sprintf("Named turn %d as %q.", meta.Turn, meta.Name)
}

// FormatRewindCommand handles /rewind <turn|name>.
func FormatRewindCommand(application *App, sessionID, workDir, args string) string {
	if application == nil {
		return "Application is not available."
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "main"
	}
	args = strings.TrimSpace(args)
	if args == "" {
		return "Usage: /rewind <turn|name>\nList checkpoints with /checkpoints.\nName one with /checkpoint-name <name> [turn]."
	}

	fields := strings.Fields(args)
	if len(fields) > 0 && strings.EqualFold(fields[0], "save") {
		return "Naming moved to /checkpoint-name <name> [turn].\nList with /checkpoints."
	}

	turn, err := strconv.Atoi(args)
	if err == nil {
		if turn < 0 {
			return "Usage: /rewind <turn|name>"
		}
		return formatRewindResult(application, sessionID, workDir, turn, "")
	}

	result, resolved, err := application.RewindCodeByName(sessionID, workDir, args)
	if err != nil {
		return fmt.Sprintf("Rewind failed: %v\nUsage: /rewind <turn|name>", err)
	}
	return formatRewindRestoreMessage(resolved, args, result)
}

func formatRewindResult(application *App, sessionID, workDir string, turn int, name string) string {
	result, err := application.RewindCode(sessionID, workDir, turn)
	if err != nil {
		return fmt.Sprintf("Rewind failed: %v", err)
	}
	return formatRewindRestoreMessage(turn, name, result)
}

func formatRewindRestoreMessage(turn int, name string, result checkpoint.RestoreResult) string {
	var parts []string
	if len(result.Restored) > 0 {
		parts = append(parts, fmt.Sprintf("restored %d file(s)", len(result.Restored)))
	}
	if len(result.Deleted) > 0 {
		parts = append(parts, fmt.Sprintf("deleted %d file(s)", len(result.Deleted)))
	}
	if len(result.Skipped) > 0 {
		parts = append(parts, fmt.Sprintf("skipped %d", len(result.Skipped)))
	}
	if len(result.Errors) > 0 {
		parts = append(parts, fmt.Sprintf("%d error(s)", len(result.Errors)))
	}
	label := fmt.Sprintf("turn %d", turn)
	if strings.TrimSpace(name) != "" {
		label = fmt.Sprintf("%q (turn %d)", name, turn)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Rewound code to %s: nothing to change.", label)
	}
	msg := fmt.Sprintf("Rewound code to %s: %s.", label, strings.Join(parts, ", "))
	if len(result.Errors) > 0 {
		msg += "\n" + strings.Join(result.Errors, "\n")
	}
	return msg
}
