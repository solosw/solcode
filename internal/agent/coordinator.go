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
	AgentIdle              AgentState = "idle"
	// AgentCompleted is the source-compatible name for a completed, reusable agent.
	AgentCompleted   AgentState = AgentIdle
	AgentInterrupted AgentState = "interrupted"
	AgentArchived    AgentState = "archived"
	AgentCancelled   AgentState = "cancelled"
	AgentFailed      AgentState = "failed"
)

type AgentConfig struct {
	// Detached runs belong to the coordinator, not to a tool invocation.
	Detached                bool   `json:"detached,omitempty"`
	AgentType               string `json:"agent_type,omitempty"`
	RoleInstructions        string `json:"role_instructions,omitempty"`
	ParentSessionID         string
	RunID                   string
	ExecutionID             string
	Attempt                 int
	ForkContext             string
	ForkLastN               int
	ParentContext           Snapshot `json:"-"`
	AcknowledgeInterruption bool
	RecoveryNote            string `json:"-"`
	ID                      AgentID
	ParentID                AgentID
	Role                    AgentRole
	Description             string
	WorkDir                 string
	Prompt                  string
	AllowedTools            []string
	MaxTurns                int
	UnlimitedTurns          bool
	Model                   string
	ParentToolUseID         string
	TaskID                  string
}

type EventKind string

const (
	EventStarted   EventKind = "started"
	EventCompleted EventKind = "completed"
	EventFailed    EventKind = "failed"
	EventCancelled EventKind = "cancelled"
)

type Event struct {
	Detached        bool
	Kind            EventKind
	Status          AgentStatus
	Result          AgentResult
	Description     string
	ParentToolUseID string
	TaskID          string
}

type AgentResult struct {
	RunID   string  `json:"run_id,omitempty"`
	NoRetry bool    `json:"no_retry,omitempty"`
	AgentID AgentID `json:"agent_id"`
	Output  string  `json:"output,omitempty"`
	Error   string  `json:"error,omitempty"`
}

type AgentStatus struct {
	AgentType       string     `json:"agent_type,omitempty"`
	RunID           string     `json:"run_id"`
	ParentSessionID string     `json:"parent_session_id,omitempty"`
	WorkDir         string     `json:"work_dir,omitempty"`
	Description     string     `json:"description,omitempty"`
	ID              AgentID    `json:"agent_id"`
	ParentID        AgentID    `json:"parent_id,omitempty"`
	Role            AgentRole  `json:"role"`
	State           AgentState `json:"state"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
}

type Runner interface {
	Run(ctx context.Context, cfg AgentConfig) AgentResult
}

type Coordinator struct {
	mu            sync.RWMutex
	runner        Runner
	agents        map[AgentID]*runningAgent
	eventHandler  func(Event)
	store         Store
	closed        bool
	notifications []Event
}

type runningAgent struct {
	checkpointMu sync.Mutex
	status       AgentStatus
	result       AgentResult
	cancel       context.CancelFunc
	done         chan struct{}
	record       *Record
	fork         Snapshot
}

func NewCoordinator(runner Runner, stores ...Store) *Coordinator {
	c := &Coordinator{runner: runner, agents: make(map[AgentID]*runningAgent)}
	if len(stores) > 0 {
		c.store = stores[0]
	}
	return c
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
	return c.start(ctx, cfg, false)
}

func (c *Coordinator) Resume(ctx context.Context, cfg AgentConfig) (AgentID, error) {
	return c.start(ctx, cfg, true)
}

func (c *Coordinator) Wait(ctx context.Context, id AgentID) (AgentResult, error) {
	c.mu.RLock()
	running := c.agents[id]
	c.mu.RUnlock()
	if running == nil {
		c.mu.RLock()
		record, err := c.load(ctx, id)
		c.mu.RUnlock()
		if err != nil {
			return AgentResult{}, err
		}
		return c.WaitRun(ctx, id, record.Status.RunID)
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

func (c *Coordinator) Cancel(id AgentID) error { return c.cancelRun(id, "") }

func (c *Coordinator) cancelRun(id AgentID, runID string) error {
	c.mu.Lock()
	running := c.agents[id]
	if running == nil {
		c.mu.Unlock()
		return fmt.Errorf("agent not found: %s", id)
	}
	if runID != "" && running.status.RunID != runID {
		c.mu.Unlock()
		return fmt.Errorf("run %s has been superseded", runID)
	}
	if !active(running) || running.status.State == AgentCancelled {
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
	for _, status := range c.listLocked() {
		if status.ParentID == parentID {
			children = append(children, status)
		}
	}
	return children
}

func (c *Coordinator) List() []AgentStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.listLocked()
}
