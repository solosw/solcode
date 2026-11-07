package app

import (
	"context"
	"fmt"

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

func (a *App) configureContextSummary(cfg *engine.Config) {
	cfg.CompactionTriggerPercent = a.Config.Memory.CompactionTriggerPercent
	if a.Config.OrganizerEnabled() {
		cfg.ContextSummary = a.summarizeContext
	}
}

func (a *App) summarizeContext(ctx context.Context, transcript string) (string, error) {
	writer := a.sessionSummaryWriter()
	if writer == nil {
		return "", fmt.Errorf("configure and enable memory.organizer to generate a context summary")
	}
	return writer.Summarize(ctx, "", transcript)
}
