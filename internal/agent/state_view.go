package agent

import (
	"context"
	"errors"
)

// listLocked preserves the legacy no-error API; model-facing listing uses
// Records so storage errors are reported rather than hidden.
func (c *Coordinator) listLocked() []AgentStatus {
	if c.store != nil {
		if records, err := c.store.List(context.Background()); err == nil {
			statuses := make([]AgentStatus, 0, len(records))
			for _, r := range records {
				_ = c.refreshState(context.Background(), r)
				statuses = append(statuses, r.Status)
			}
			return statuses
		}
	}
	statuses := make([]AgentStatus, 0, len(c.agents))
	for _, a := range c.agents {
		statuses = append(statuses, a.status)
	}
	return statuses
}

// refreshState is called with the registry lock held. A saved running flag is
// not proof of a live worker after restart; probe the process-owned writer lock.
func (c *Coordinator) refreshState(ctx context.Context, r *Record) error {
	if a := c.agents[r.Status.ID]; active(a) && a.status.RunID == r.Status.RunID {
		r.Status = a.status
		return nil
	}
	if r.Status.State != AgentRunning && r.Status.State != AgentWaitingPermission {
		return nil
	}
	if c.store == nil {
		return nil
	}
	release, err := c.store.Acquire(ctx, r.Status.ID)
	if errors.Is(err, ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = release(); err != nil {
		return err
	}
	r.Status.State = AgentInterrupted
	return nil
}
