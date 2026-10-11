package tool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Background job lifecycle states returned by Wait / Bash.
const (
	JobRunning   = "running"
	JobCompleted = "completed"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
	JobTimedOut  = "timed_out"
)

// JobSnapshot is a point-in-time view of a background shell job.
type JobSnapshot struct {
	ID         string
	Command    string
	WorkDir    string
	Status     string
	ExitCode   int
	Stdout     string
	Stderr     string
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	Elapsed    time.Duration
}

// BackgroundJobs tracks long-running shell commands started when Bash timeout
// exceeds AutoWaitThresholdMs (or legacy run_in_background). Bash blocks on
// Wait for the same duration; the Wait tool itself is not model-visible.
type BackgroundJobs struct {
	mu      sync.Mutex
	seq     atomic.Uint64
	jobs    map[string]*backgroundJob
	order   []string
	maxKeep int
}

type backgroundJob struct {
	id      string
	command string
	workDir string

	mu         sync.Mutex
	status     string
	exitCode   int
	errText    string
	startedAt  time.Time
	finishedAt time.Time
	stdout     *safeBuffer
	stderr     *safeBuffer

	cancel context.CancelFunc
	done   chan struct{}
}

type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// defaultBackgroundJobs is the process-wide registry used by Bash and Wait.
var defaultBackgroundJobs = NewBackgroundJobs(64)

// DefaultBackgroundJobs returns the shared job registry.
func DefaultBackgroundJobs() *BackgroundJobs {
	return defaultBackgroundJobs
}

// NewBackgroundJobs creates an empty registry. maxKeep bounds retained finished jobs.
func NewBackgroundJobs(maxKeep int) *BackgroundJobs {
	if maxKeep <= 0 {
		maxKeep = 64
	}
	return &BackgroundJobs{
		jobs:    make(map[string]*backgroundJob),
		maxKeep: maxKeep,
	}
}

// Start registers and launches a background command. run must block until the
// process exits; it receives live stdout/stderr writers and a cancellable ctx.
func (m *BackgroundJobs) Start(command, workDir string, timeout time.Duration, run func(ctx context.Context, stdout, stderr *safeBuffer) (exitCode int, execErr error)) string {
	return m.StartWithContext(context.Background(), command, workDir, timeout, run)
}

// StartWithContext binds the command lifetime to its owning agent run as well
// as its timeout. Canceling a foreground wait cannot leave an orphaned process.
func (m *BackgroundJobs) StartWithContext(parent context.Context, command, workDir string, timeout time.Duration, run func(ctx context.Context, stdout, stderr *safeBuffer) (exitCode int, execErr error)) string {
	if m == nil {
		return ""
	}
	if timeout <= 0 {
		timeout = time.Duration(MaxTimeout) * time.Millisecond
	}
	id := fmt.Sprintf("bash-%d", m.seq.Add(1))
	ctx, cancel := context.WithTimeout(parent, timeout)
	job := &backgroundJob{
		id:        id,
		command:   command,
		workDir:   workDir,
		status:    JobRunning,
		startedAt: time.Now(),
		stdout:    &safeBuffer{},
		stderr:    &safeBuffer{},
		cancel:    cancel,
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.jobs[id] = job
	m.order = append(m.order, id)
	m.trimLocked()
	m.mu.Unlock()

	go func() {
		defer close(job.done)
		defer cancel()
		code, err := run(ctx, job.stdout, job.stderr)
		job.mu.Lock()
		defer job.mu.Unlock()
		job.finishedAt = time.Now()
		job.exitCode = code
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			job.status = JobTimedOut
			job.errText = "command timed out"
		case ctx.Err() == context.Canceled:
			job.status = JobCanceled
			job.errText = "command canceled"
		case err != nil:
			job.status = JobFailed
			job.errText = err.Error()
		case code != 0:
			job.status = JobFailed
			job.errText = fmt.Sprintf("exit code %d", code)
		default:
			job.status = JobCompleted
		}
	}()

	return id
}

// Snapshot returns a copy of job state, or false if unknown.
func (m *BackgroundJobs) Snapshot(id string) (JobSnapshot, bool) {
	if m == nil {
		return JobSnapshot{}, false
	}
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil {
		return JobSnapshot{}, false
	}
	return job.snapshot(), true
}

// List returns snapshots in start order (oldest first).
func (m *BackgroundJobs) List() []JobSnapshot {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	ids := append([]string(nil), m.order...)
	m.mu.Unlock()
	out := make([]JobSnapshot, 0, len(ids))
	for _, id := range ids {
		if snap, ok := m.Snapshot(id); ok {
			out = append(out, snap)
		}
	}
	return out
}

// RunningIDs returns ids still in the running state.
func (m *BackgroundJobs) RunningIDs() []string {
	var out []string
	for _, snap := range m.List() {
		if snap.Status == JobRunning {
			out = append(out, snap.ID)
		}
	}
	return out
}

// Wait blocks until the job finishes, the wait timeout elapses, or ctx is done.
// timeout<=0 means wait until the job completes (still bounded by ctx).
func (m *BackgroundJobs) Wait(ctx context.Context, id string, timeout time.Duration) (JobSnapshot, error) {
	if m == nil {
		return JobSnapshot{}, fmt.Errorf("background job manager is nil")
	}
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil {
		return JobSnapshot{}, fmt.Errorf("unknown background task_id %q", id)
	}

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}

	select {
	case <-job.done:
		return job.snapshot(), nil
	case <-timer:
		snap := job.snapshot()
		return snap, errWaitTimeout
	case <-ctx.Done():
		return job.snapshot(), ctx.Err()
	}
}

// WaitAny waits until every currently-running job finishes, or timeout/ctx fires.
// If there are no running jobs, it returns immediately with the full list.
func (m *BackgroundJobs) WaitAny(ctx context.Context, timeout time.Duration) ([]JobSnapshot, error) {
	ids := m.RunningIDs()
	if len(ids) == 0 {
		return m.List(), nil
	}

	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	var lastErr error
	for _, id := range ids {
		remain := time.Duration(0)
		if !deadline.IsZero() {
			remain = time.Until(deadline)
			if remain <= 0 {
				lastErr = errWaitTimeout
				break
			}
		}
		if _, err := m.Wait(ctx, id, remain); err != nil {
			lastErr = err
			if err == errWaitTimeout || ctx.Err() != nil {
				break
			}
		}
	}
	return m.List(), lastErr
}

// Cancel requests termination of a running job.
func (m *BackgroundJobs) Cancel(id string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil || job.cancel == nil {
		return false
	}
	job.cancel()
	return true
}

func (m *BackgroundJobs) trimLocked() {
	for len(m.order) > m.maxKeep {
		oldest := m.order[0]
		job := m.jobs[oldest]
		if job != nil {
			job.mu.Lock()
			running := job.status == JobRunning
			job.mu.Unlock()
			if running {
				// Keep running jobs; stop trimming to avoid dropping live work.
				return
			}
			delete(m.jobs, oldest)
		}
		m.order = m.order[1:]
	}
}

func (j *backgroundJob) snapshot() JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	finished := j.finishedAt
	elapsed := time.Since(j.startedAt)
	if !finished.IsZero() {
		elapsed = finished.Sub(j.startedAt)
	}
	return JobSnapshot{
		ID:         j.id,
		Command:    j.command,
		WorkDir:    j.workDir,
		Status:     j.status,
		ExitCode:   j.exitCode,
		Stdout:     TruncateOutput(j.stdout.String(), MaxOutputLength),
		Stderr:     TruncateOutput(j.stderr.String(), MaxOutputLength),
		Error:      j.errText,
		StartedAt:  j.startedAt,
		FinishedAt: finished,
		Elapsed:    elapsed,
	}
}

var errWaitTimeout = fmt.Errorf("wait timed out")

func formatJobSnapshot(snap JobSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "task_id: %s\n", snap.ID)
	fmt.Fprintf(&b, "status: %s\n", snap.Status)
	fmt.Fprintf(&b, "command: %s\n", snap.Command)
	fmt.Fprintf(&b, "elapsed: %s\n", snap.Elapsed.Round(time.Millisecond))
	if snap.ExitCode != 0 || snap.Status == JobFailed || snap.Status == JobTimedOut {
		fmt.Fprintf(&b, "exit_code: %d\n", snap.ExitCode)
	}
	if snap.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", snap.Error)
	}
	if snap.Stdout != "" {
		b.WriteString("\n--- stdout ---\n")
		b.WriteString(snap.Stdout)
		if !strings.HasSuffix(snap.Stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if snap.Stderr != "" {
		b.WriteString("\n--- stderr ---\n")
		b.WriteString(snap.Stderr)
		if !strings.HasSuffix(snap.Stderr, "\n") {
			b.WriteByte('\n')
		}
	}
	if snap.Stdout == "" && snap.Stderr == "" && snap.Status != JobRunning {
		b.WriteString("\n(no output)\n")
	}
	if snap.Status == JobRunning {
		b.WriteString("\n(still running — raise Bash timeout or re-run if the command needs more time)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
