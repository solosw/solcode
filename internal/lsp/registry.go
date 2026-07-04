package lsp

import "fmt"

var ErrServerUnavailable = fmt.Errorf("language server is not available")

type ServerCommand struct {
	Language   string   `json:"language"`
	Extensions []string `json:"extensions"`
	Command    []string `json:"command"`
}

type Registry struct {
	commands []ServerCommand
}

func NewRegistry(commands ...ServerCommand) *Registry {
	return &Registry{commands: append([]ServerCommand(nil), commands...)}
}

func (r *Registry) Register(command ServerCommand) {
	r.commands = append(r.commands, command)
}

func (r *Registry) Commands() []ServerCommand {
	return append([]ServerCommand(nil), r.commands...)
}
