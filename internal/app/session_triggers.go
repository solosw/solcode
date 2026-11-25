package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/tool"
	"github.com/solosw/solcode/internal/trigger"
)

type triggerScopeKey struct{}
type sessionTriggerState struct {
	runtime *trigger.Runtime
	notify  func()
	cancel  context.CancelFunc
}

func triggerKey(id, dir string) string { return id + "\x00" + dir }

func (a *App) triggerStateLocked(id, dir string) (*sessionTriggerState, error) {
	key := triggerKey(id, dir)
	if state := a.triggerSessions[key]; state != nil {
		return state, nil
	}
	storeDir := ""
	if a.Sessions != nil {
		storeDir = a.Config.Session.Dir
	}
	state := &sessionTriggerState{}
	runtime, err := trigger.New(id, dir, storeDir, func() {
		a.triggerMu.Lock()
		notify := state.notify
		a.triggerMu.Unlock()
		if notify != nil {
			notify()
		}
	})
	if err != nil {
		return nil, err
	}
	state.runtime = runtime
	if a.triggerSessions == nil {
		a.triggerSessions = map[string]*sessionTriggerState{}
	}
	a.triggerSessions[key] = state
	return state, nil
}

// AttachSessionTriggers connects the current live host to its session's listener
// queue. notify must return promptly; the host drains only when it is idle.
func (a *App) AttachSessionTriggers(id, dir string, notify func()) (func(), error) {
	a.triggerMu.Lock()
	state, err := a.triggerStateLocked(id, dir)
	if err != nil {
		a.triggerMu.Unlock()
		return nil, err
	}
	state.notify = notify
	a.triggerMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.triggerMu.Lock()
			if a.triggerSessions[triggerKey(id, dir)] != state {
				a.triggerMu.Unlock()
				return
			}
			delete(a.triggerSessions, triggerKey(id, dir))
			state.notify = nil
			cancel := state.cancel
			a.triggerMu.Unlock()
			if cancel != nil {
				cancel()
			}
			state.runtime.Close()
		})
	}, nil
}

func (a *App) bindSessionTriggers(ctx context.Context, id, dir string) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	a.triggerMu.Lock()
	state, err := a.triggerStateLocked(id, dir)
	if err != nil {
		a.triggerMu.Unlock()
		cancel()
		return ctx, func() {}, err
	}
	if state.cancel != nil {
		a.triggerMu.Unlock()
		cancel()
		return ctx, func() {}, fmt.Errorf("session is already running")
	}
	state.cancel = cancel
	a.triggerMu.Unlock()
	return context.WithValue(ctx, triggerScopeKey{}, state), func() {
		cancel()
		a.triggerMu.Lock()
		state.cancel = nil
		notify := state.notify
		a.triggerMu.Unlock()
		if notify != nil {
			notify()
		}
	}, nil
}

func (a *App) handleSolAPI(ctx context.Context, id, dir string, req tool.SolAPIRequest) (any, error) {
	state, _ := ctx.Value(triggerScopeKey{}).(*sessionTriggerState)
	a.triggerMu.Lock()
	current := a.triggerSessions[triggerKey(id, dir)]
	notify := false
	if state != nil {
		notify = state.notify != nil
	}
	a.triggerMu.Unlock()
	if state == nil || current != state {
		return nil, fmt.Errorf("solapi is outside the current session")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch req.Action {
	case "register":
		if !notify {
			return nil, fmt.Errorf("this host cannot wake idle sessions")
		}
		return state.runtime.Register(ctx, req.Code)
	case "history":
		limit := req.Limit
		if limit == 0 {
			limit = 20
		}
		records, total := state.runtime.History(req.Offset, limit)
		return map[string]any{"records": records, "total": total, "offset": req.Offset, "next_offset": min(total, req.Offset+len(records))}, nil
	case "cancel":
		return map[string]any{"cancelled": true}, a.CancelSessionTriggers(id, dir)
	default:
		return nil, fmt.Errorf("unknown solapi action")
	}
}

// CancelSessionTriggers stops only the selected session and keeps its transcript.
func (a *App) CancelSessionTriggers(id, dir string) error {
	a.triggerMu.Lock()
	state := a.triggerSessions[triggerKey(id, dir)]
	var cancel context.CancelFunc
	if state != nil {
		cancel = state.cancel
	}
	a.triggerMu.Unlock()
	if state == nil {
		return nil
	}
	return state.runtime.CancelRun(cancel)
}

func (a *App) NextSessionTrigger(id, dir string) (trigger.Event, bool) {
	a.triggerMu.Lock()
	state := a.triggerSessions[triggerKey(id, dir)]
	a.triggerMu.Unlock()
	if state == nil {
		return trigger.Event{}, false
	}
	return state.runtime.Next()
}

func (a *App) RunSessionTrigger(ctx context.Context, event trigger.Event, maxTurns int) (agent.AgentResult, error) {
	a.triggerMu.Lock()
	state := a.triggerSessions[triggerKey(event.SessionID, event.WorkDir)]
	a.triggerMu.Unlock()
	if state == nil {
		return agent.AgentResult{}, fmt.Errorf("trigger session is closed")
	}
	if err := state.runtime.Mark(event, "running", nil); err != nil {
		return agent.AgentResult{}, err
	}
	result, err := a.RunPromptWithSession(ctx, event.SessionID, event.Prompt, event.WorkDir, maxTurns)
	status := "completed"
	cause := err
	if cause == nil && result.Error != "" {
		cause = fmt.Errorf("%s", result.Error)
	}
	if cause != nil {
		status = "failed"
	}
	if ctx.Err() != nil || result.Error == context.Canceled.Error() {
		status = "cancelled"
	}
	if historyErr := state.runtime.Mark(event, status, cause); err == nil && historyErr != nil && historyErr != context.Canceled {
		err = historyErr
	}
	return result, err
}

func (a *App) FailSessionTrigger(event trigger.Event, cause error) {
	a.triggerMu.Lock()
	state := a.triggerSessions[triggerKey(event.SessionID, event.WorkDir)]
	a.triggerMu.Unlock()
	if state != nil {
		_ = state.runtime.Mark(event, "failed", cause)
	}
}

func (a *App) closeSessionTriggers() {
	a.triggerMu.Lock()
	states := a.triggerSessions
	a.triggerSessions = nil
	for _, state := range states {
		state.notify = nil
		if state.cancel != nil {
			state.cancel()
		}
	}
	a.triggerMu.Unlock()
	for _, state := range states {
		state.runtime.Close()
	}
}
