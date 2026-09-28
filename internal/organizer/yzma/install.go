package yzma

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"

	getter "github.com/hashicorp/go-getter"
	"github.com/hybridgroup/yzma/pkg/download"
)

// InstallState is the coarse lifecycle of a library install.
type InstallState string

const (
	InstallIdle      InstallState = "idle"
	InstallRunning   InstallState = "running"
	InstallDone      InstallState = "done"
	InstallFailed    InstallState = "failed"
	InstallCancelled InstallState = "cancelled"
)

// InstallStatus is a snapshot of library-install progress.
//
// Bytes/TotalBytes come from the downloader and may be zero when the server
// does not report a Content-Length; Percent is then -1 and callers should show
// an indeterminate indicator.
type InstallStatus struct {
	State      InstallState `json:"state"`
	Processor  string       `json:"processor"`
	Version    string       `json:"version"`
	Bytes      int64        `json:"bytes"`
	TotalBytes int64        `json:"total_bytes"`
	// Percent is 0..100, or -1 when the total is unknown.
	Percent float64 `json:"percent"`
	// Message is a human-readable line for the UI.
	Message string `json:"message"`
	// Error is set when State is InstallFailed.
	Error string `json:"error"`
	// LibDir is where the libraries are being installed.
	LibDir string `json:"lib_dir"`
	// StartedAt/FinishedAt bound the run.
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Installer downloads llama.cpp shared libraries with observable progress.
//
// Only one install runs at a time: the libraries land in a shared directory and
// a second concurrent install would race on the same files. A redundant request
// while one is running is rejected rather than queued, because the UI already
// polls status and can simply wait.
type Installer struct {
	mu       sync.Mutex
	status   InstallStatus
	cancel   context.CancelFunc
	running  bool
	onChange func(InstallStatus)
}

// NewInstaller builds an idle installer.
func NewInstaller() *Installer {
	return &Installer{status: InstallStatus{State: InstallIdle, Percent: -1}}
}

// SetOnChange registers a callback invoked on each status change. It is called
// without the internal lock held.
func (i *Installer) SetOnChange(fn func(InstallStatus)) {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.onChange = fn
	i.mu.Unlock()
}

// Status returns the current install snapshot.
func (i *Installer) Status() InstallStatus {
	if i == nil {
		return InstallStatus{State: InstallIdle, Percent: -1}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.status
}

// Running reports whether an install is in flight.
func (i *Installer) Running() bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.running
}

// Start begins a background install into libDir.
//
// It returns an error when one is already running. The returned context governs
// the download; cancel it through Cancel or by cancelling the parent.
func (i *Installer) Start(parent context.Context, libDir, processor string) error {
	if i == nil {
		return fmt.Errorf("yzma: installer is nil")
	}
	libDir = strings.TrimSpace(libDir)
	if libDir == "" {
		return fmt.Errorf("yzma: library directory is required")
	}
	proc, err := download.ParseProcessor(strings.ToLower(strings.TrimSpace(processor)))
	if err != nil {
		return fmt.Errorf("yzma: unknown processor %q: %w", processor, err)
	}

	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return fmt.Errorf("yzma: a library install is already running")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	i.cancel = cancel
	i.running = true
	i.status = InstallStatus{
		State:     InstallRunning,
		Processor: proc.String(),
		LibDir:    libDir,
		Percent:   -1,
		Message:   "Resolving llama.cpp release…",
		StartedAt: time.Now(),
	}
	snapshot := i.status
	i.mu.Unlock()
	i.emit(snapshot)

	go i.run(ctx, libDir, proc)
	return nil
}

// Cancel stops a running install. It is a no-op when idle.
func (i *Installer) Cancel() {
	if i == nil {
		return
	}
	i.mu.Lock()
	cancel := i.cancel
	i.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (i *Installer) run(ctx context.Context, libDir string, proc download.Processor) {
	target := download.Target{
		Arch:      download.MustParseArch(runtime.GOARCH),
		OS:        download.MustParseOS(runtime.GOOS),
		Processor: proc,
	}
	// Let yzma pick the CUDA build matching this machine when asked for CUDA.
	if proc.Equal(download.CUDA) {
		if ok, version := download.HasCUDA(); ok {
			target.CUDAVersion = version
		}
	}

	tracker := i.newProgressTracker(ctx)
	err := download.Install(ctx, target, libDir, tracker, nil)

	i.mu.Lock()
	i.running = false
	i.cancel = nil
	status := i.status
	status.FinishedAt = time.Now()
	switch {
	case err == nil:
		status.State = InstallDone
		status.Percent = 100
		if status.TotalBytes > 0 {
			status.Bytes = status.TotalBytes
		}
		status.Message = "llama.cpp libraries installed."
		status.Error = ""
	case ctx.Err() != nil:
		status.State = InstallCancelled
		status.Message = "Install cancelled."
		status.Error = ""
	default:
		status.State = InstallFailed
		status.Message = "Install failed."
		status.Error = err.Error()
	}
	i.status = status
	i.mu.Unlock()
	i.emit(status)
}

func (i *Installer) emit(status InstallStatus) {
	i.mu.Lock()
	fn := i.onChange
	i.mu.Unlock()
	if fn != nil {
		fn(status)
	}
}

// progressTracker reports download progress into the installer status.
type progressTracker struct {
	installer *Installer
	ctx       context.Context
}

func (i *Installer) newProgressTracker(ctx context.Context) getter.ProgressTracker {
	return &progressTracker{installer: i, ctx: ctx}
}

// TrackProgress wraps the download stream and counts bytes as they arrive.
//
// go-getter calls this once per asset. Assets download sequentially, so byte
// counts are accumulated per asset and the total is treated as unknown rather
// than guessed: reporting a wrong percentage is worse than reporting none.
func (t *progressTracker) TrackProgress(src string, currentSize, totalSize int64, stream io.ReadCloser) io.ReadCloser {
	t.installer.setProgress(src, totalSize, 0)
	return &countingReader{
		reader:  stream,
		total:   totalSize,
		onBytes: func(n int64) { t.installer.setProgress(src, totalSize, n) },
	}
}

func (i *Installer) setProgress(src string, total, read int64) {
	i.mu.Lock()
	if i.status.State != InstallRunning {
		i.mu.Unlock()
		return
	}
	i.status.TotalBytes = total
	i.status.Bytes = read
	if total > 0 {
		pct := float64(read) / float64(total) * 100
		if pct > 100 {
			pct = 100
		}
		i.status.Percent = pct
	} else {
		i.status.Percent = -1
	}
	if name := assetName(src); name != "" {
		i.status.Message = "Downloading " + name
	}
	snapshot := i.status
	i.mu.Unlock()
	i.emit(snapshot)
}

// countingReader counts bytes read and reports the running total.
type countingReader struct {
	reader  io.ReadCloser
	total   int64
	read    int64
	onBytes func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if n > 0 {
		c.read += int64(n)
		if c.onBytes != nil {
			c.onBytes(c.read)
		}
	}
	return n, err
}

func (c *countingReader) Close() error { return c.reader.Close() }

// assetName reduces a download URL to its last path segment for display.
func assetName(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	src = strings.TrimRight(src, "/")
	if index := strings.LastIndex(src, "/"); index >= 0 {
		src = src[index+1:]
	}
	// Strip a query string if one is present.
	if index := strings.Index(src, "?"); index >= 0 {
		src = src[:index]
	}
	return src
}

var _ getter.ProgressTracker = (*progressTracker)(nil)
