package trigger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

const maxRecords = 1000

// Event is a host-owned capability. JavaScript never supplies its session scope.
type Event struct {
	ID         string    `json:"id"`
	ListenerID string    `json:"listener_id,omitempty"`
	SessionID  string    `json:"session_id"`
	WorkDir    string    `json:"work_dir"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Prompt     string    `json:"prompt,omitempty"`
	Error      string    `json:"error,omitempty"`
	StopReason string    `json:"stop_reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Runtime struct {
	mu                       sync.Mutex
	sessionID, workDir, path string
	notify                   func()
	records                  []Event
	queue                    []string
	listeners                map[string]context.CancelFunc
	closed                   bool
	wg                       sync.WaitGroup
	closeOnce                sync.Once
}

func New(sessionID, workDir, storeDir string, notify func()) (*Runtime, error) {
	r := &Runtime{sessionID: sessionID, workDir: workDir, notify: notify, listeners: map[string]context.CancelFunc{}}
	if storeDir != "" {
		key := sha256.Sum256([]byte(sessionID + "\x00" + workDir))
		r.path = filepath.Join(storeDir, "triggers", hex.EncodeToString(key[:])+".json")
		data, err := os.ReadFile(r.path)
		if err == nil {
			if err = json.Unmarshal(data, &r.records); err != nil {
				return nil, fmt.Errorf("read trigger history: %w", err)
			}
			for i := range r.records {
				e := &r.records[i]
				if e.SessionID != sessionID || e.WorkDir != workDir {
					return nil, fmt.Errorf("trigger history has mismatched scope")
				}
				if e.Status == "listening" || e.Status == "queued" || e.Status == "running" {
					e.Status = "interrupted"
					e.UpdatedAt = time.Now().UTC()
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return r, nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (r *Runtime) persistLocked() error {
	for len(r.records) > maxRecords {
		for i, event := range r.records {
			if event.Status != "listening" && event.Status != "queued" && event.Status != "running" {
				r.records = append(r.records[:i], r.records[i+1:]...)
				break
			}
		}
	}
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(r.records)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(r.path), ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), r.path)
}
func (r *Runtime) event(kind, status, prompt, parent string) Event {
	now := time.Now().UTC()
	return Event{ID: newID(), ListenerID: parent, SessionID: r.sessionID, WorkDir: r.workDir, Kind: kind, Status: status, Prompt: prompt, CreatedAt: now, UpdatedAt: now}
}

func (r *Runtime) Register(admission context.Context, code string) (Event, error) {
	program, err := goja.Compile("listener.js", "(async function(){\n"+code+"\n})()", false)
	if err != nil {
		return Event{}, fmt.Errorf("invalid listener JavaScript: %w", err)
	}
	r.mu.Lock()
	if err := admission.Err(); err != nil {
		r.mu.Unlock()
		return Event{}, err
	}
	if r.closed {
		r.mu.Unlock()
		return Event{}, fmt.Errorf("session listeners are closed")
	}
	if len(r.listeners) >= 16 {
		r.mu.Unlock()
		return Event{}, fmt.Errorf("at most 16 listeners per session")
	}
	e := r.event("listener", "listening", "", "")
	r.records = append(r.records, e)
	if err = r.persistLocked(); err != nil {
		r.records = r.records[:len(r.records)-1]
		r.mu.Unlock()
		return Event{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.listeners[e.ID] = cancel
	r.wg.Add(1)
	r.mu.Unlock()
	go func() { defer r.wg.Done(); r.run(ctx, e.ID, program) }()
	return e, nil
}

func (r *Runtime) run(ctx context.Context, id string, program *goja.Program) {
	vm := goja.New()
	// A listener can sleep indefinitely, but a CPU-only loop cannot hold a VM forever.
	watchdog := time.AfterFunc(time.Second, func() { vm.Interrupt("listener exceeded one second without yielding") })
	defer watchdog.Stop()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-done:
		}
	}()
	defer close(done)
	fail := func(err error) { panic(vm.NewGoError(err)) }
	sol := vm.NewObject()
	_ = sol.Set("sleep", func(call goja.FunctionCall) goja.Value {
		ms := call.Argument(0).ToInteger()
		if ms < 25 || ms > 86400000 {
			fail(fmt.Errorf("sleep requires 25..86400000 milliseconds"))
		}
		watchdog.Stop()
		timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			fail(ctx.Err())
		}
		watchdog.Reset(time.Second)
		return goja.Undefined()
	})
	_ = sol.Set("readText", func(call goja.FunctionCall) goja.Value {
		if ctx.Err() != nil {
			fail(ctx.Err())
		}
		path := call.Argument(0).String()
		if filepath.IsAbs(path) {
			fail(fmt.Errorf("readText requires a project-relative path"))
		}
		root, err := os.OpenRoot(r.workDir)
		if err != nil {
			fail(err)
		}
		defer root.Close()
		info, err := root.Stat(path)
		if os.IsNotExist(err) {
			return goja.Null()
		}
		if err != nil {
			fail(err)
		}
		if !info.Mode().IsRegular() {
			fail(fmt.Errorf("readText accepts regular files only"))
		}
		f, err := root.Open(path)
		if os.IsNotExist(err) {
			return goja.Null()
		}
		if err != nil {
			fail(err)
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
		if err != nil {
			fail(err)
		}
		if len(data) > 1024*1024 {
			fail(fmt.Errorf("readText exceeds 1 MiB"))
		}
		return vm.ToValue(string(data))
	})
	_ = sol.Set("trigger", func(call goja.FunctionCall) goja.Value {
		if ctx.Err() != nil {
			fail(ctx.Err())
		}
		prompt := strings.TrimSpace(call.Argument(0).String())
		if prompt == "" || len(prompt) > 65536 {
			fail(fmt.Errorf("trigger prompt must be 1..65536 bytes"))
		}
		r.mu.Lock()
		if r.closed || r.listeners[id] == nil {
			r.mu.Unlock()
			fail(fmt.Errorf("listener cancelled"))
		}
		if len(r.queue) >= 64 {
			r.mu.Unlock()
			fail(fmt.Errorf("session trigger queue is full"))
		}
		e := r.event("trigger", "queued", prompt, id)
		r.records = append(r.records, e)
		if err := r.persistLocked(); err != nil {
			r.records = r.records[:len(r.records)-1]
			r.mu.Unlock()
			fail(err)
		}
		r.queue = append(r.queue, e.ID)
		notify := r.notify
		r.mu.Unlock()
		if notify != nil {
			notify()
		}
		return vm.ToValue(e.ID)
	})
	_ = vm.Set("sol", sol)
	value, err := vm.RunProgram(program)
	if err == nil {
		if promise, ok := value.Export().(*goja.Promise); ok {
			if promise.State() == goja.PromiseStateRejected {
				err = fmt.Errorf("%s", promise.Result())
			}
			if promise.State() == goja.PromiseStatePending {
				err = fmt.Errorf("listener has an unresolved promise")
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.listeners, id)
	for i := range r.records {
		e := &r.records[i]
		if e.ID != id {
			continue
		}
		if e.Status == "listening" {
			e.Status = "completed"
			if err != nil {
				e.Status = "failed"
				e.Error = err.Error()
			}
			if ctx.Err() != nil {
				e.Status = "cancelled"
			}
			e.UpdatedAt = time.Now().UTC()
		}
	}
	_ = r.persistLocked()
}

func (r *Runtime) Next() (Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.queue) > 0 {
		id := r.queue[0]
		r.queue = r.queue[1:]
		for _, e := range r.records {
			if e.ID == id && e.Status == "queued" {
				return e, true
			}
		}
	}
	return Event{}, false
}

// Mark rejects forged/cross-session events and only transitions queued/running work.
func (r *Runtime) Mark(e Event, status string, cause error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.SessionID != r.sessionID || e.WorkDir != r.workDir {
		return fmt.Errorf("trigger is outside current session")
	}
	for i := range r.records {
		current := &r.records[i]
		if current.ID != e.ID {
			continue
		}
		if status == "running" && current.Status != "queued" {
			return fmt.Errorf("trigger is no longer queued")
		}
		if current.Status == "cancelled" {
			return context.Canceled
		}
		if current.Status == "completed" && current.StopReason == "manual_stop" {
			return nil
		}
		current.Status = status
		current.UpdatedAt = time.Now().UTC()
		if cause != nil {
			current.Error = cause.Error()
		}
		return r.persistLocked()
	}
	return fmt.Errorf("unknown session trigger")
}

func (r *Runtime) Cancel() error {
	return r.CancelRun(nil)
}

// CancelRun serializes run cancellation with listener admission and final records.
func (r *Runtime) CancelRun(stop context.CancelFunc) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, cancel := range r.listeners {
		cancel()
	}
	clear(r.listeners)
	r.queue = nil
	r.records = append(r.records, r.event("cancel", "cancelled", "", ""))
	for i := range r.records {
		e := &r.records[i]
		if e.Status == "listening" || e.Status == "queued" || e.Status == "running" {
			if e.Kind == "trigger" && e.Status == "running" && !r.closed {
				e.Status = "completed"
				e.StopReason = "manual_stop"
				e.Error = ""
			} else {
				e.Status = "cancelled"
			}
			e.UpdatedAt = time.Now().UTC()
		}
	}
	return r.persistLocked()
}

func (r *Runtime) Close() {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		_ = r.Cancel()
		r.wg.Wait()
	})
}

func (r *Runtime) History(offset, limit int) ([]Event, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := len(r.records)
	if offset > total {
		offset = total
	}
	end := min(total, offset+limit)
	out := make([]Event, end-offset)
	for i := range out {
		out[i] = r.records[total-1-offset-i]
	}
	return out, total
}
