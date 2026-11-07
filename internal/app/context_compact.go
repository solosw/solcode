package app

import (
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
)

// Reload the selected organizer along with feature settings. Otherwise a newly
// configured API endpoint leaves summary generation using the previous runtime.
func (a *App) reloadOrganizer(cfg config.Config) {
	previous := a.organizer
	a.organizer = newOrganizerRuntime(cfg)
	if previous != nil {
		_ = previous.Close()
	}
}

func (a *App) configureContextCompression(cfg *engine.Config) {
	cfg.CompactionTriggerPercent = a.Config.Memory.CompactionTriggerPercent
}
