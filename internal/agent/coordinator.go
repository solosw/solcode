package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type AgentID string

type AgentRole string

const (
	AgentRoleMain AgentRole = "main"
	AgentRoleSub  AgentRole = "sub"
	AgentRoleTask AgentRole = "task"
)

type AgentState string

const (
	AgentCreated           AgentState = "created"
	AgentRunning           AgentState = "running"
	AgentWaitingPermission AgentState = "waiting_permission"
	AgentCompleted         AgentState = "completed"
	AgentCancelled         AgentState = "cancelled"
	AgentFailed            AgentState = "failed"
)

type AgentConfig struct {
	ID           AgentID
	ParentID     AgentID
	Role         AgentRole
	Description  string
	WorkDir      string
	Prompt       string
	AllowedTools []string
	MaxTurns     int
}

type EventKind string

const (
	EventStarted   EventKind = "started"
	EventCompleted EventKind = "completed"
	EventFailed    EventKind = "failed"
	EventCancelled EventKind = "cancelled"
)

type Event struct {
	Kind        EventKind
	Status      AgentStatus
	Result      AgentResult
	Description string
}

type AgentResult struct {
	AgentID AgentID
	Output  string
	Error   string
}

type AgentStatus struct {
	ID        AgentID
	ParentID  AgentID
	Role      AgentRole
	State     AgentState
	StartedAt time.Time
	EndedAt   *time.Time
}

type Runner interface {
	Run(ctx context.Context, cfg AgentConfig) AgentResult
}

type Coordinator struct {
	mu           sync.RWMutex
	runner       Runner
	agents       map[AgentID]*runningAgent
	eventHandler func(Event)
}

type runningAgent struct {
	status AgentStatus
	result AgentResult
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCoordinator(runner Runner) *Coordinator {
	return &Coordinator{
		runner: runner,
		agents: make(map[AgentID]*runningAgent),
	}
}

func (c *Coordinator) SetEventHandler(handler func(Event)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.eventHandler = handler
	c.mu.Unlock()
}

func (c *Coordinator) Spawn(ctx context.Context, cfg AgentConfig) (AgentID, error) {
	if cfg.ID == "" {
		return "", fmt.Errorf("agent id is required")
	}

	c.mu.Lock()
	if _, exists := c.agents[cfg.ID]; exists {
		c.mu.Unlock()
		return "", fmt.Errorf("agent already exists: %s", cfg.ID)
	}
	runCtx, cancel := context.WithCancel(ctx)
	running := &runningAgent{
		status: AgentStatus{
			ID:        cfg.ID,
			ParentID:  cfg.ParentID,
			Role:      cfg.Role,
			State:     AgentRunning,
			StartedAt: time.Now(),
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	c.agents[cfg.ID] = running
	status := running.status
	description := cfg.Description
	handler := c.eventHandler
	c.mu.Unlock()
	c.emit(handler, Event{Kind: EventStarted, Status: status, Description: description})

	go func() {
		result := c.runner.Run(runCtx, cfg)
		result.AgentID = cfg.ID
		now := time.Now()
		var event Event
		var handler func(Event)

		c.mu.Lock()
		running.result = result
		running.status.EndedAt = &now
		if running.status.State != AgentCancelled {
			if result.Error != "" {
				running.status.State = AgentFailed
			} else {
				running.status.State = AgentCompleted
			}
		}
		event = Event{
			Status:      running.status,
			Result:      result,
			Description: description,
		}
		switch running.status.State {
		case AgentCancelled:
			event.Kind = EventCancelled
		case AgentFailed:
			event.Kind = EventFailed
		default:
			event.Kind = EventCompleted
		}
		handler = c.eventHandler
		close(running.done)
		c.mu.Unlock()
		c.emit(handler, event)
	}()

	return cfg.ID, nil
}

func (c *Coordinator) Wait(ctx context.Context, id AgentID) (AgentResult, error) {
	c.mu.RLock()
	running := c.agents[id]
	c.mu.RUnlock()
	if running == nil {
		return AgentResult{}, fmt.Errorf("agent not found: %s", id)
	}

	select {
	case <-ctx.Done():
		return AgentResult{}, ctx.Err()
	case <-running.done:
		c.mu.RLock()
		result := running.result
		c.mu.RUnlock()
		return result, nil
	}
}

func (c *Coordinator) Cancel(id AgentID) error {
	c.mu.Lock()
	running := c.agents[id]
	if running == nil {
		c.mu.Unlock()
		return fmt.Errorf("agent not found: %s", id)
	}
	if running.status.State == AgentCompleted || running.status.State == AgentFailed || running.status.State == AgentCancelled {
		c.mu.Unlock()
		return nil
	}
	running.status.State = AgentCancelled
	now := time.Now()
	running.status.EndedAt = &now
	cancel := running.cancel
	c.mu.Unlock()

	cancel()
	return nil
}

func (c *Coordinator) emit(handler func(Event), event Event) {
	if handler != nil {
		handler(event)
	}
}

func (c *Coordinator) Children(parentID AgentID) []AgentStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	children := make([]AgentStatus, 0)
	for _, running := range c.agents {
		if running.status.ParentID == parentID {
			children = append(children, running.status)
		}
	}
	return children
}

func (c *Coordinator) List() []AgentStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	statuses := make([]AgentStatus, 0, len(c.agents))
	for _, running := range c.agents {
		statuses = append(statuses, running.status)
	}
	return statuses
}
