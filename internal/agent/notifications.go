package agent

import "path/filepath"

// DrainNotifications consumes this parent session's detached-run completions.
// Other sessions and projects retain their own mailbox entries. Notifications
// are process-local; durable results remain accessible through status/history.
func (c *Coordinator) DrainNotifications(scope Scope) []Event {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var events []Event
	remaining := c.notifications[:0]
	for _, event := range c.notifications {
		status := event.Status
		if status.ParentSessionID == scope.ParentSessionID && filepath.Clean(status.WorkDir) == filepath.Clean(scope.WorkDir) && (scope.ParentSessionID != "" || status.ParentID == scope.ParentID) {
			events = append(events, event)
		} else {
			remaining = append(remaining, event)
		}
	}
	clear(c.notifications[len(remaining):])
	c.notifications = remaining
	return events
}

// Close cancels and joins owned runs before their shared tools are closed.
func (c *Coordinator) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	var running []*runningAgent
	for _, a := range c.agents {
		if active(a) {
			running = append(running, a)
		}
	}
	c.mu.Unlock()
	for _, a := range running {
		a.cancel()
	}
	for _, a := range running {
		<-a.done
	}
}
