package app

import (
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/tool"
)

// Use the same write capabilities at startup and after a settings reload.
func registerMemoryWriters(registry *tool.Registry, a *App, cfg config.Config) {
	if cfg.OrganizerEnabled() {
		return
	}
	var archival tool.MemoryWriter
	if cfg.Memory.Enabled {
		archival = a
	}
	registry.Register(tool.NewWriteMemoryTool(archival, a))
	// Retain execution of saved tool calls, but hide this legacy name from models.
	registry.Register(tool.NewWriteSessionMemoryTool(a))
}
