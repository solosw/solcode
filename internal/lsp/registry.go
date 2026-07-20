package lsp

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrServerUnavailable = fmt.Errorf("language server is not available")

// ServerCommand describes how to launch a language server for a set of file extensions.
type ServerCommand struct {
	Language   string   `json:"language"`
	Extensions []string `json:"extensions"`
	Command    []string `json:"command"`
}

// Registry maps file extensions to language-server launch commands.
type Registry struct {
	commands []ServerCommand
}

// NewRegistry creates a registry from the given commands (no filtering).
func NewRegistry(commands ...ServerCommand) *Registry {
	return &Registry{commands: normalizeCommands(commands)}
}

// BuildRegistry merges user-configured servers with optional defaults and
// keeps only commands whose binary is present on PATH.
func BuildRegistry(user []ServerCommand, includeDefaults bool) *Registry {
	merged := mergeCommands(user, includeDefaults)
	return NewRegistry(FilterAvailable(merged)...)
}

// Register adds a server command.
func (r *Registry) Register(command ServerCommand) {
	if r == nil {
		return
	}
	normalized := normalizeCommands([]ServerCommand{command})
	if len(normalized) == 0 {
		return
	}
	r.commands = append(r.commands, normalized...)
}

// Commands returns a copy of registered server commands.
func (r *Registry) Commands() []ServerCommand {
	if r == nil {
		return nil
	}
	return append([]ServerCommand(nil), r.commands...)
}

// Lookup finds a server command for the given file path by extension.
func (r *Registry) Lookup(filePath string) (ServerCommand, bool) {
	if r == nil {
		return ServerCommand{}, false
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		return ServerCommand{}, false
	}
	for _, cmd := range r.commands {
		for _, e := range cmd.Extensions {
			if strings.ToLower(e) == ext {
				return cmd, true
			}
		}
	}
	return ServerCommand{}, false
}

// LookupLanguage finds a server command by language id.
func (r *Registry) LookupLanguage(language string) (ServerCommand, bool) {
	if r == nil {
		return ServerCommand{}, false
	}
	language = strings.ToLower(strings.TrimSpace(language))
	for _, cmd := range r.commands {
		if strings.ToLower(cmd.Language) == language {
			return cmd, true
		}
	}
	return ServerCommand{}, false
}

// FilterAvailable keeps only commands whose executable is on PATH.
func FilterAvailable(commands []ServerCommand) []ServerCommand {
	out := make([]ServerCommand, 0, len(commands))
	for _, cmd := range commands {
		if len(cmd.Command) == 0 {
			continue
		}
		if _, err := exec.LookPath(cmd.Command[0]); err != nil {
			continue
		}
		out = append(out, cmd)
	}
	return out
}

func mergeCommands(user []ServerCommand, includeDefaults bool) []ServerCommand {
	user = normalizeCommands(user)
	if !includeDefaults {
		return user
	}
	// User commands override defaults for the same language or overlapping extensions.
	coveredLang := map[string]bool{}
	coveredExt := map[string]bool{}
	for _, cmd := range user {
		coveredLang[strings.ToLower(cmd.Language)] = true
		for _, e := range cmd.Extensions {
			coveredExt[strings.ToLower(e)] = true
		}
	}
	out := append([]ServerCommand(nil), user...)
	for _, def := range DefaultServerCommands() {
		if coveredLang[strings.ToLower(def.Language)] {
			continue
		}
		overlap := false
		for _, e := range def.Extensions {
			if coveredExt[strings.ToLower(e)] {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		out = append(out, def)
	}
	return out
}

func normalizeCommands(commands []ServerCommand) []ServerCommand {
	out := make([]ServerCommand, 0, len(commands))
	for _, cmd := range commands {
		cmd.Language = strings.TrimSpace(cmd.Language)
		if cmd.Language == "" && len(cmd.Command) > 0 {
			cmd.Language = cmd.Command[0]
		}
		exts := make([]string, 0, len(cmd.Extensions))
		seen := map[string]bool{}
		for _, e := range cmd.Extensions {
			e = strings.TrimSpace(strings.ToLower(e))
			if e == "" {
				continue
			}
			if !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			if seen[e] {
				continue
			}
			seen[e] = true
			exts = append(exts, e)
		}
		cmd.Extensions = exts
		args := make([]string, 0, len(cmd.Command))
		for _, a := range cmd.Command {
			a = strings.TrimSpace(a)
			if a != "" {
				args = append(args, a)
			}
		}
		cmd.Command = args
		if cmd.Language == "" || len(cmd.Command) == 0 || len(cmd.Extensions) == 0 {
			continue
		}
		out = append(out, cmd)
	}
	return out
}
