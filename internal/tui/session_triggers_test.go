package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Run a returned batch as Bubble Tea would, but leave spinner ticks out of the
// update loop so the test observes only the completion of the actual work.
func triggerCompletion(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing trigger command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			if msg := triggerCompletion(t, child); msg != nil {
				return msg
			}
		}
		return nil
	}
	if _, ok := msg.(spinnerTickMsg); ok {
		return nil
	}
	return msg
}

func TestSessionTriggerReservesBusyStateAndDrainsAfterCompletion(t *testing.T) {
	m := New(func(string) (tea.Cmd, func()) {
		t.Fatal("user prompt started a second run")
		return nil, nil
	})
	queued, dequeued, runs, userMessages := 2, 0, 0, 0
	m.SetQueueFunc(func(string) { userMessages++ })
	m.SetSessionTriggers(func() uint64 { return 1 }, func() (tea.Cmd, func()) {
		if queued == 0 {
			return nil, nil
		}
		queued--
		dequeued++
		return func() tea.Msg { runs++; return StreamDoneMsg{} }, func() {}
	}, nil)
	m.streaming = true
	for range 2 {
		next, cmd := m.Update(SessionTriggerReadyMsg{Scope: 1})
		m = next.(Model)
		if cmd != nil || dequeued != 0 {
			t.Fatal("listener dequeued while the user run was active")
		}
	}
	next, first := m.Update(StreamDoneMsg{})
	m = next.(Model)
	if !m.streaming || dequeued != 1 || runs != 0 {
		t.Fatalf("work was not reserved synchronously: streaming=%v dequeued=%d runs=%d", m.streaming, dequeued, runs)
	}
	m.input.SetValue("a follow-up while the trigger command is pending")
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	next, _ = m.Update(SessionTriggerReadyMsg{Scope: 1})
	m = next.(Model)
	if userMessages != 1 || dequeued != 1 {
		t.Fatal("a prompt or duplicate wake bypassed the reserved busy state")
	}
	next, second := m.Update(triggerCompletion(t, first))
	m = next.(Model)
	if !m.streaming || dequeued != 2 || runs != 1 {
		t.Fatal("pending listener event was lost at completion")
	}
	next, _ = m.Update(triggerCompletion(t, second))
	m = next.(Model)
	if m.streaming || runs != 2 || queued != 0 {
		t.Fatal("trigger queue did not drain serially")
	}
}

func TestSessionTriggerRejectsOldAttachmentIncludingSwitchBack(t *testing.T) {
	m := New(nil)
	scope, dequeued := uint64(1), 0
	m.SetSessionTriggers(func() uint64 { return scope }, func() (tea.Cmd, func()) {
		dequeued++
		return func() tea.Msg { return StreamDoneMsg{} }, func() {}
	}, nil)
	m.streaming = true
	next, _ := m.Update(SessionTriggerReadyMsg{Scope: 1})
	m = next.(Model)
	// Switch inside the dialog callback to exercise the scope change during
	// Update, including returning to an earlier session with a fresh attachment.
	m.streaming = false
	m.dialog = &DialogState{Active: DialogSessions, Items: []DialogItem{{Value: "original-session"}}}
	m.SetDialogCallbacks(nil, func(DialogKind, string) SelectResult {
		scope = 3
		return SelectResult{ReplaceMessages: true}
	})
	next, switchCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if switchCmd != nil || dequeued != 0 || m.streaming {
		t.Fatal("pending wake survived a session switch inside Update")
	}
	for _, msg := range []tea.Msg{StatusTextMsg{Text: "Switched session"}, SessionTriggerReadyMsg{Scope: 1}, SessionTriggerReadyMsg{Scope: 2}} {
		next, cmd := m.Update(msg)
		m = next.(Model)
		if cmd != nil || dequeued != 0 || m.streaming {
			t.Fatal("old attachment woke the newly selected session")
		}
	}
	next, cmd := m.Update(SessionTriggerReadyMsg{Scope: 3})
	m = next.(Model)
	if cmd == nil || dequeued != 1 || !m.streaming {
		t.Fatal("current attachment could not wake")
	}
}

func TestSessionTriggerWaitsForIdleUI(t *testing.T) {
	for name, busy := range map[string]func(*Model){
		"async command":   func(m *Model) { m.spinnerActive = true },
		"permission":      func(m *Model) { m.pending = &pendingPermission{} },
		"confirmation":    func(m *Model) { m.pendingConf = &pendingConfirm{} },
		"question":        func(m *Model) { m.pendingAsk = &pendingAskUser{} },
		"session dialog":  func(m *Model) { m.dialog = &DialogState{Active: DialogSessions} },
		"workflow editor": func(m *Model) { m.workflowEditor = &WorkflowEditorState{} },
	} {
		t.Run(name, func(t *testing.T) {
			m := New(nil)
			dequeued := 0
			m.SetSessionTriggers(func() uint64 { return 1 }, func() (tea.Cmd, func()) {
				dequeued++
				return func() tea.Msg { return StreamDoneMsg{} }, func() {}
			}, nil)
			busy(&m)
			next, cmd := m.Update(SessionTriggerReadyMsg{Scope: 1})
			m = next.(Model)
			if cmd != nil || dequeued != 0 {
				t.Fatal("listener started while the UI was busy")
			}
			m.spinnerActive = false
			m.pending, m.pendingConf, m.pendingAsk, m.dialog, m.workflowEditor = nil, nil, nil, nil, nil
			next, cmd = m.Update(CommandResultMsg{Text: "Finished"})
			m = next.(Model)
			if cmd == nil || dequeued != 1 || !m.streaming {
				t.Fatal("busy wake was not retained until the UI became idle")
			}
		})
	}
}

func TestSessionTriggerCompletionAfterErrorDoesNotLosePendingEvent(t *testing.T) {
	for name, completion := range map[string]tea.Msg{
		"error":     StreamErrorMsg{Err: errors.New("model failed")},
		"cancelled": StreamCanceledMsg{Reason: "Canceled"},
	} {
		t.Run(name, func(t *testing.T) {
			m := New(nil)
			m.streaming = true
			dequeued := 0
			m.SetSessionTriggers(func() uint64 { return 1 }, func() (tea.Cmd, func()) {
				dequeued++
				return nil, nil
			}, nil)
			next, _ := m.Update(SessionTriggerReadyMsg{Scope: 1})
			m = next.(Model)
			next, _ = m.Update(completion)
			m = next.(Model)
			if dequeued != 1 || m.streaming {
				t.Fatal("terminal result did not check the pending queue")
			}
		})
	}
}

func TestSessionTriggerCancelStopsListenersAndDiscardsPendingWake(t *testing.T) {
	for _, active := range []bool{false, true} {
		m := New(nil)
		m.streaming = active
		m.cancelCurrent = func() {}
		queued, cancelled, dequeued := true, 0, 0
		m.SetSessionTriggers(func() uint64 { return 1 }, func() (tea.Cmd, func()) {
			dequeued++
			if queued {
				t.Fatal("cancelled listener event was started")
			}
			return nil, nil
		}, func() { queued = false; cancelled++ })
		m.dialog = &DialogState{Active: DialogSessions}
		next, _ := m.Update(SessionTriggerReadyMsg{Scope: 1})
		m = next.(Model)
		m.dialog = nil
		next, _ = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		m = next.(Model)
		if queued || cancelled != 1 || dequeued != 0 {
			t.Fatalf("cancel failed: queued=%v cancelled=%d dequeued=%d", queued, cancelled, dequeued)
		}
		if active {
			next, _ = m.Update(StreamCanceledMsg{})
			m = next.(Model)
			if m.streaming || dequeued != 1 {
				t.Fatal("cancelled run did not return idle with an empty queue")
			}
		}
	}
}
