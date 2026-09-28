package workflowui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/workflow"
)

// libraryServer starts a server whose install callbacks are observable fakes.
func libraryServer(t *testing.T, status LibraryInstallStatus) (*Server, string, *fakeInstaller) {
	t.Helper()
	installer := &fakeInstaller{status: status}
	srv, url, err := Start(Config{
		WorkDir: t.TempDir(),
		List:    func() []workflow.Definition { return nil },
		Save: func(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error) {
			return "", nil
		},
		Settings:                      func() config.Config { return config.Default() },
		ApplySettings:                 func(next config.Config) error { return nil },
		MemoryOrganizerLibraryStatus:  installer.Status,
		MemoryOrganizerLibraryInstall: installer.Install,
		MemoryOrganizerLibraryCancel:  installer.Cancel,
	})
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, url, installer
}

type fakeInstaller struct {
	mu             sync.Mutex
	status         LibraryInstallStatus
	installCalls   int
	cancelCalls    int
	installErr     error
	lastProcessor  string
	cancelObserved bool
}

func (f *fakeInstaller) Status() LibraryInstallStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeInstaller) Install(processor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installCalls++
	f.lastProcessor = processor
	if f.installErr != nil {
		return f.installErr
	}
	f.status = LibraryInstallStatus{State: "running", Processor: processor, Percent: -1}
	return nil
}

func (f *fakeInstaller) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	f.cancelObserved = true
	f.status = LibraryInstallStatus{State: "cancelled"}
}

func (f *fakeInstaller) calls() (install, cancel int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installCalls, f.cancelCalls
}

func TestMemoryOrganizerLibraryStatusEndpoint(t *testing.T) {
	_, url, _ := libraryServer(t, LibraryInstallStatus{
		State:          "idle",
		Percent:        -1,
		LibDir:         "/tmp/llama",
		LibraryPresent: false,
	})

	res, err := http.Get(url + "/api/memory-organizer/library")
	if err != nil {
		t.Fatalf("GET library status: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got := body["state"]; got != "idle" {
		t.Fatalf("state = %#v, want idle", got)
	}
	if got := body["percent"]; got != float64(-1) {
		t.Fatalf("percent = %#v, want -1", got)
	}
	if got := body["lib_dir"]; got != "/tmp/llama" {
		t.Fatalf("lib_dir = %#v, want /tmp/llama", got)
	}
}

func TestMemoryOrganizerLibraryInstallStartsBackgroundDownload(t *testing.T) {
	_, url, installer := libraryServer(t, LibraryInstallStatus{State: "idle", Percent: -1})

	raw, err := json.Marshal(map[string]any{"processor": "vulkan"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(url+"/api/memory-organizer/library", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST library install: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	// The handler must return immediately with a running snapshot, not block on
	// the download.
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode install response: %v", err)
	}
	if got := body["state"]; got != "running" {
		t.Fatalf("state = %#v, want running", got)
	}
	if install, _ := installer.calls(); install != 1 {
		t.Fatalf("install calls = %d, want 1", install)
	}
	installer.mu.Lock()
	processor := installer.lastProcessor
	installer.mu.Unlock()
	if processor != "vulkan" {
		t.Fatalf("processor = %q, want vulkan", processor)
	}
}

// An empty body is valid: the server falls back to the configured processor.
func TestMemoryOrganizerLibraryInstallAcceptsEmptyBody(t *testing.T) {
	_, url, installer := libraryServer(t, LibraryInstallStatus{State: "idle", Percent: -1})
	res, err := http.Post(url+"/api/memory-organizer/library", "application/json", nil)
	if err != nil {
		t.Fatalf("POST library install: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 for an empty body", res.StatusCode)
	}
	if install, _ := installer.calls(); install != 1 {
		t.Fatalf("install calls = %d, want 1", install)
	}
}

// A concurrent install is a conflict, not a bad request: the UI distinguishes
// them so it can simply keep polling instead of showing an error.
func TestMemoryOrganizerLibraryInstallConflictWhenRunning(t *testing.T) {
	installer := &fakeInstaller{status: LibraryInstallStatus{State: "idle", Percent: -1}}
	installer.installErr = errAlreadyRunning{}
	_, url, err := startWithInstaller(t, installer)
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	res, err := http.Post(url+"/api/memory-organizer/library", "application/json", nil)
	if err != nil {
		t.Fatalf("POST library install: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a concurrent install", res.StatusCode)
	}
}

type errAlreadyRunning struct{}

func (errAlreadyRunning) Error() string {
	return "yzma: a library install is already running"
}

func startWithInstaller(t *testing.T, installer *fakeInstaller) (*Server, string, error) {
	t.Helper()
	srv, url, err := Start(Config{
		WorkDir: t.TempDir(),
		List:    func() []workflow.Definition { return nil },
		Save: func(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error) {
			return "", nil
		},
		Settings:                      func() config.Config { return config.Default() },
		ApplySettings:                 func(next config.Config) error { return nil },
		MemoryOrganizerLibraryStatus:  installer.Status,
		MemoryOrganizerLibraryInstall: installer.Install,
		MemoryOrganizerLibraryCancel:  installer.Cancel,
	})
	if err != nil {
		return nil, "", err
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, url, nil
}

func TestMemoryOrganizerLibraryInstallRejectsBadRequest(t *testing.T) {
	installer := &fakeInstaller{status: LibraryInstallStatus{State: "idle", Percent: -1}}
	installer.installErr = errBadProcessor{}
	_, url, err := startWithInstaller(t, installer)
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	res, err := http.Post(url+"/api/memory-organizer/library", "application/json", nil)
	if err != nil {
		t.Fatalf("POST library install: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown processor", res.StatusCode)
	}
}

type errBadProcessor struct{}

func (errBadProcessor) Error() string {
	return `yzma: unknown processor "nope"`
}

func TestMemoryOrganizerLibraryCancelEndpoint(t *testing.T) {
	_, url, installer := libraryServer(t, LibraryInstallStatus{State: "running", Percent: -1})

	req, err := http.NewRequest(http.MethodDelete, url+"/api/memory-organizer/library", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE library: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if got := body["state"]; got != "cancelled" {
		t.Fatalf("state = %#v, want cancelled", got)
	}
	if _, cancel := installer.calls(); cancel != 1 {
		t.Fatalf("cancel calls = %d, want 1", cancel)
	}
}

// The endpoint must be explicit when the feature is not wired, so the UI can
// hide the control rather than show a broken button.
func TestMemoryOrganizerLibraryNotConfigured(t *testing.T) {
	srv, url, err := Start(Config{
		WorkDir: t.TempDir(),
		List:    func() []workflow.Definition { return nil },
		Save: func(def workflow.Definition, scope workflow.SaveScope, layout *workflow.Layout) (string, error) {
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	res, err := http.Get(url + "/api/memory-organizer/library")
	if err != nil {
		t.Fatalf("GET library status: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when install is not configured", res.StatusCode)
	}
}

func TestMemoryOrganizerLibraryRejectsUnsupportedMethod(t *testing.T) {
	_, url, _ := libraryServer(t, LibraryInstallStatus{State: "idle", Percent: -1})
	req, err := http.NewRequest(http.MethodPut, url+"/api/memory-organizer/library", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT library: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
}
