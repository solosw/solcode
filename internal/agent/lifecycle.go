package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	contextmessage "github.com/solosw/solcode/internal/message"
)

func cloneRecord(r *Record) (*Record, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var out Record
	err = json.Unmarshal(b, &out)
	return &out, err
}

func active(a *runningAgent) bool {
	if a == nil {
		return false
	}
	select {
	case <-a.done:
		return false
	default:
		return true
	}
}

func (c *Coordinator) load(ctx context.Context, id AgentID) (*Record, error) {
	if c.store != nil {
		return c.store.Load(ctx, id)
	}
	if a := c.agents[id]; a != nil {
		return cloneRecord(a.record)
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (c *Coordinator) save(ctx context.Context, r *Record) error {
	if c.store != nil {
		return c.store.Save(ctx, r)
	}
	return nil
}

func authorize(r *Record, scope Scope) error {
	if filepath.Clean(r.Config.WorkDir) != filepath.Clean(scope.WorkDir) {
		return fmt.Errorf("agent %s is outside the current project/workdir scope", r.Status.ID)
	}
	return nil
}

func (c *Coordinator) start(ctx context.Context, cfg AgentConfig, resume bool) (AgentID, error) {
	if !ValidID(string(cfg.ID)) {
		return "", fmt.Errorf("invalid agent id")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", fmt.Errorf("subagent coordinator is closed")
	}
	if active(c.agents[cfg.ID]) {
		return "", fmt.Errorf("%w: %s", ErrBusy, cfg.ID)
	}
	release := func() error { return nil }
	if c.store != nil {
		var err error
		release, err = c.store.Acquire(ctx, cfg.ID)
		if err != nil {
			return "", err
		}
	}
	started := false
	defer func() {
		if !started {
			_ = release()
		}
	}()
	r, err := c.load(ctx, cfg.ID)
	if resume && err != nil {
		return "", err
	}
	if !resume {
		if err == nil {
			return "", fmt.Errorf("agent already exists: %s", cfg.ID)
		}
		if !errors.Is(err, ErrNotFound) {
			return "", err
		}
		r = &Record{CreatedAt: time.Now(), ForkParentSessionID: cfg.ParentSessionID}
	}
	var fork Snapshot
	if resume {
		if err := authorize(r, Scope{cfg.ParentID, cfg.ParentSessionID, cfg.WorkDir}); err != nil {
			return "", err
		}
		if r.Archived {
			return "", fmt.Errorf("agent %s is archived", cfg.ID)
		}
		// Older records used the most recent parent as the fork observation source.
		// Freeze that source before switching to a different project session.
		if r.ForkParentSessionID == "" {
			r.ForkParentSessionID = r.Config.ParentSessionID
		}
		if cfg.AgentType != "" && cfg.AgentType != r.Config.AgentType {
			return "", fmt.Errorf("agent_type cannot change on resume: %s", r.Config.AgentType)
		}
		if cfg.AgentType == "" && cfg.RoleInstructions == "" {
			cfg.RoleInstructions = r.Config.RoleInstructions
		}
		cfg.AgentType = r.Config.AgentType
		if cfg.Description == "" {
			cfg.Description = r.Config.Description
		}
		cfg.Role = r.Config.Role
		cfg.AllowedTools = IntersectTools(r.Config.AllowedTools, cfg.AllowedTools)
		if r.ForkID != "" {
			if c.store != nil {
				fork, err = c.store.LoadFork(ctx, r.ForkID)
			} else {
				fork = c.agents[cfg.ID].fork
			}
			if err != nil {
				return "", err
			}
		}
	} else {
		fork, err = selectFork(cfg)
		if err != nil {
			return "", err
		}
		if len(fork.Messages) > 0 || len(fork.WorkingSet) > 0 {
			if c.store != nil {
				r.ForkID, err = c.store.SaveFork(ctx, fork)
				if err != nil {
					return "", err
				}
			} else {
				r.ForkID = "memory"
			}
			r.WorkingSet = fork.WorkingSet
			for i := range fork.Messages {
				r.Context = append(r.Context, -1-i)
			}
		}
	}
	snapshot, err := r.Active(fork)
	if err != nil {
		return "", err
	}
	pending := PendingToolIDs(snapshot.Messages)
	if len(pending) > 0 {
		if !cfg.AcknowledgeInterruption {
			return "", fmt.Errorf("%w: %v", ErrUncertain, pending)
		}
		// Preserve the original interrupted transcript. Append explicit unknown
		// results only after the caller has reconciled effects, never run old tools.
		var blocks []sdk.ContentBlockParamUnion
		for _, id := range pending {
			blocks = append(blocks, sdk.NewToolResultBlock(id, "Execution outcome unknown after interruption. Caller acknowledged reconciliation. Do not replay this call; verify external state before any new mutation.", true))
		}
		// A partial tool_result must be completed in the same user message.
		if n := len(snapshot.Messages); n > 0 && string(snapshot.Messages[n-1].Role) == "user" {
			last := snapshot.Messages[n-1]
			if len(last.Content) > 0 && last.Content[0].OfToolResult != nil {
				last.Content = append(append([]sdk.ContentBlockParamUnion(nil), last.Content...), blocks...)
				snapshot.Messages[n-1] = last
			} else {
				snapshot.Messages = append(snapshot.Messages, sdk.NewUserMessage(blocks...))
			}
		} else {
			snapshot.Messages = append(snapshot.Messages, sdk.NewUserMessage(blocks...))
		}
		cfg.RecoveryNote = "Previous execution was interrupted with unknown tool outcomes; do not replay old operations. Follow the caller's reconciliation instructions."
	}
	now := time.Now()
	if n := len(r.Runs); n > 0 && (r.Runs[n-1].State == AgentRunning || r.Runs[n-1].State == AgentWaitingPermission) {
		r.Runs[n-1].State = AgentInterrupted
		r.Runs[n-1].EndedAt = &now
		r.Runs[n-1].Result = AgentResult{AgentID: cfg.ID, RunID: r.Runs[n-1].ID, Error: "run interrupted before completion; inspect history", NoRetry: true}
		r.Events = append(r.Events, SessionEvent{RunID: r.Runs[n-1].ID, Kind: "interrupted", At: now, MessageCount: len(r.Messages)})
	}
	if cfg.RunID == "" {
		cfg.RunID = NewID("run")
	}
	for _, run := range r.Runs {
		if run.ID == cfg.RunID {
			return "", fmt.Errorf("run id already exists: %s", cfg.RunID)
		}
	}
	if cfg.ExecutionID == "" {
		cfg.ExecutionID = NewID("execution")
	}
	if cfg.Attempt <= 0 {
		cfg.Attempt = 1
	}
	// Parent snapshots and previous permissions are never carried into a resume.
	cfg.ParentContext = Snapshot{}
	r.Config = cfg
	r.Status = AgentStatus{AgentType: cfg.AgentType, ID: cfg.ID, RunID: cfg.RunID, ParentID: cfg.ParentID, ParentSessionID: cfg.ParentSessionID, WorkDir: cfg.WorkDir, Description: cfg.Description, Role: cfg.Role, State: AgentRunning, StartedAt: now}
	r.Runs = append(r.Runs, RunRecord{ID: cfg.RunID, ParentID: cfg.ParentID, ParentSessionID: cfg.ParentSessionID, ExecutionID: cfg.ExecutionID, Attempt: cfg.Attempt, TaskID: cfg.TaskID, Description: cfg.Description, Model: cfg.Model, AllowedTools: append([]string(nil), cfg.AllowedTools...), State: AgentRunning, StartedAt: now})
	r.UpdatedAt = now
	if err := r.apply(Checkpoint{Snapshot: snapshot, Stage: "started"}, fork); err != nil {
		return "", err
	}
	if err := c.save(ctx, r); err != nil {
		return "", err
	}
	runParent := ctx
	if cfg.Detached {
		runParent = context.WithoutCancel(ctx)
	}
	runCtx, cancel := context.WithCancel(runParent)
	a := &runningAgent{status: r.Status, record: r, fork: fork, cancel: cancel, done: make(chan struct{})}
	c.agents[cfg.ID] = a
	handler := c.eventHandler
	startedStatus := a.status
	started = true
	// Start events are emitted outside the registry lock, before runner work.
	go func() {
		c.emit(handler, Event{Detached: cfg.Detached, Kind: EventStarted, Status: startedStatus, Description: cfg.Description, ParentToolUseID: cfg.ParentToolUseID, TaskID: cfg.TaskID})
		c.execute(runCtx, a, cfg, release, snapshot)
	}()
	return cfg.ID, nil
}

func (c *Coordinator) execute(ctx context.Context, a *runningAgent, cfg AgentConfig, release func() error, snapshot Snapshot) {
	defer a.cancel()
	checkpoint := func(cp Checkpoint) error {
		a.checkpointMu.Lock()
		defer a.checkpointMu.Unlock()
		c.mu.RLock()
		current, fork := a.record, a.fork
		c.mu.RUnlock()
		next, err := cloneRecord(current)
		if err != nil {
			return err
		}
		if err = next.apply(cp, fork); err != nil {
			return err
		}
		// Cancellation must still durably record a tool result that just returned.
		if err = c.save(context.WithoutCancel(ctx), next); err != nil {
			return err
		}
		c.mu.Lock()
		a.record = next
		c.mu.Unlock()
		return nil
	}
	var result AgentResult
	if runner, ok := c.runner.(PersistentRunner); ok {
		result = runner.RunSession(ctx, SessionRequest{Config: cfg, Snapshot: snapshot, Save: checkpoint})
	} else {
		// Compatibility runners have no tool execution or recoverable message loop.
		messages := snapshot.Messages
		if cfg.Attempt <= 1 || len(messages) == 0 {
			messages = append(messages, sdk.NewUserMessage(sdk.NewTextBlock(cfg.Prompt)))
		}
		if err := checkpoint(Checkpoint{Snapshot: Snapshot{Messages: messages}, Stage: "prompt"}); err != nil {
			result = AgentResult{Error: err.Error(), NoRetry: true}
		} else {
			result = c.runner.Run(ctx, cfg)
			if result.Output != "" {
				messages = append(messages, sdk.NewAssistantMessage(sdk.NewTextBlock(result.Output)))
			}
			if err := checkpoint(Checkpoint{Snapshot: Snapshot{Messages: messages}, Stage: "response"}); err != nil {
				result.Error = err.Error()
				result.NoRetry = true
			}
		}
	}
	result.AgentID = cfg.ID
	result.RunID = cfg.RunID
	c.mu.Lock()
	now := time.Now()
	if ctx.Err() != nil {
		a.status.State = AgentCancelled
	}
	if a.status.State == AgentCancelled {
		if result.Error == "" {
			result.Error = context.Canceled.Error()
		}
		result.NoRetry = true
	}
	a.status.EndedAt = &now
	if a.status.State != AgentCancelled {
		if result.Error != "" {
			a.status.State = AgentFailed
		} else {
			a.status.State = AgentIdle
		}
	}
	a.record.Status = a.status
	a.record.Result = result
	a.record.UpdatedAt = now
	n := len(a.record.Runs) - 1
	a.record.Runs[n].State = a.status.State
	a.record.Runs[n].Result = result
	a.record.Runs[n].EndedAt = &now
	a.record.Events = append(a.record.Events, SessionEvent{RunID: cfg.RunID, Kind: string(a.status.State), At: now, MessageCount: len(a.record.Messages)})
	if err := c.save(context.WithoutCancel(ctx), a.record); err != nil {
		result.Error = fmt.Sprintf("persist agent completion: %v", err)
		result.NoRetry = true
		a.status.State = AgentFailed
	}
	if err := release(); err != nil {
		result.Error = fmt.Sprintf("release agent: %v", err)
		result.NoRetry = true
	}
	a.result = result
	handler := c.eventHandler
	event := Event{Detached: cfg.Detached, Kind: EventCompleted, Status: a.status, Result: result, Description: cfg.Description, ParentToolUseID: cfg.ParentToolUseID, TaskID: cfg.TaskID}
	if a.status.State == AgentFailed {
		event.Kind = EventFailed
	}
	if a.status.State == AgentCancelled {
		event.Kind = EventCancelled
	}
	if c.store != nil {
		a.record = nil
		a.fork = Snapshot{}
	}
	if cfg.Detached {
		c.notifications = append(c.notifications, event)
	}
	close(a.done)
	c.mu.Unlock()
	c.emit(handler, event)
}

func (c *Coordinator) Get(ctx context.Context, id AgentID, scope Scope) (*Record, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, err := c.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = authorize(r, scope); err != nil {
		return nil, err
	}
	if err = c.refreshState(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (c *Coordinator) Records(ctx context.Context, scope Scope) ([]*Record, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var records []*Record
	if c.store != nil {
		var err error
		records, err = c.store.List(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		for _, a := range c.agents {
			r, err := cloneRecord(a.record)
			if err != nil {
				return nil, err
			}
			records = append(records, r)
		}
	}
	out := records[:0]
	for _, r := range records {
		if authorize(r, scope) == nil {
			if err := c.refreshState(ctx, r); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (c *Coordinator) Archive(ctx context.Context, id AgentID, scope Scope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if active(c.agents[id]) {
		return ErrBusy
	}
	release := func() error { return nil }
	if c.store != nil {
		var err error
		release, err = c.store.Acquire(ctx, id)
		if err != nil {
			return err
		}
	}
	defer release()
	r, err := c.load(ctx, id)
	if err != nil {
		return err
	}
	if err = authorize(r, scope); err != nil {
		return err
	}
	r.Archived = true
	r.Status.State = AgentArchived
	r.UpdatedAt = time.Now()
	r.Events = append(r.Events, SessionEvent{RunID: r.Status.RunID, Kind: "archived", At: r.UpdatedAt, MessageCount: len(r.Messages)})
	if err = c.save(ctx, r); err != nil {
		return err
	}
	if a := c.agents[id]; a != nil {
		if c.store == nil {
			a.record = r
		}
		a.status = r.Status
	}
	return nil
}

func selectFork(cfg AgentConfig) (Snapshot, error) {
	switch cfg.ForkContext {
	case "", "none":
		return Snapshot{}, nil
	case "working_set":
		return Snapshot{WorkingSet: cfg.ParentContext.WorkingSet}, nil
	case "full":
		return cfg.ParentContext, nil
	case "last_n":
		n := cfg.ForkLastN
		if n <= 0 {
			n = 10
		}
		messages := cfg.ParentContext.Messages
		start := 0
		for i := len(messages) - 1; i >= 0; i-- {
			m := messages[i]
			if string(m.Role) == "user" && !contextmessage.IsDynamicContext(m) && !contextmessage.IsBackgroundToolNotification(m) {
				hasResult := false
				for _, b := range m.Content {
					if b.OfToolResult != nil {
						hasResult = true
					}
				}
				if !hasResult {
					n--
					if n == 0 {
						start = i
						break
					}
				}
			}
		}
		return Snapshot{Messages: messages[start:], WorkingSet: cfg.ParentContext.WorkingSet}, nil
	default:
		return Snapshot{}, fmt.Errorf("invalid fork_context: %s", cfg.ForkContext)
	}
}
