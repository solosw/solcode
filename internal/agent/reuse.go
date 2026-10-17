package agent

import (
	"context"
	"fmt"
)

// IntersectTools never turns an empty intersection into unrestricted execution.
// An empty request preserves the original ceiling; current global permissions
// are still checked by the executor on every invocation.
func IntersectTools(ceiling, requested []string) []string {
	if len(ceiling) == 0 {
		return append([]string(nil), requested...)
	}
	if len(requested) == 0 {
		return append([]string(nil), ceiling...)
	}
	allowed := map[string]bool{}
	for _, name := range ceiling {
		allowed[name] = true
	}
	var out []string
	for _, name := range requested {
		if allowed[name] {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return []string{"__no_tools__"}
	}
	return out
}

func (c *Coordinator) WaitRun(ctx context.Context, id AgentID, runID string) (AgentResult, error) {
	c.mu.RLock()
	a := c.agents[id]
	if a != nil && a.status.RunID == runID {
		c.mu.RUnlock()
		select {
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		case <-a.done:
		}
		c.mu.RLock()
		result := a.result
		c.mu.RUnlock()
		return result, nil
	}
	record, err := c.load(ctx, id)
	c.mu.RUnlock()
	if err != nil {
		return AgentResult{}, err
	}
	for _, run := range record.Runs {
		if run.ID == runID {
			if run.EndedAt == nil {
				return AgentResult{}, fmt.Errorf("run %s was interrupted or is owned by another process", runID)
			}
			return run.Result, nil
		}
	}
	return AgentResult{}, fmt.Errorf("run not found: %s", runID)
}

func (c *Coordinator) CancelRun(id AgentID, runID string) error {
	if runID == "" {
		return fmt.Errorf("run_id is required")
	}
	return c.cancelRun(id, runID)
}

func (c *Coordinator) History(ctx context.Context, id AgentID, scope Scope) (Snapshot, error) {
	record, err := c.Get(ctx, id, scope)
	if err != nil {
		return Snapshot{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var inherited Snapshot
	if record.ForkID != "" {
		if c.store != nil {
			inherited, err = c.store.LoadFork(ctx, record.ForkID)
		} else {
			inherited = c.agents[id].fork
		}
		if err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{Messages: append(inherited.Messages, record.Messages...), WorkingSet: record.WorkingSet}, nil
}
