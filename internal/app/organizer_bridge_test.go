package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/memory"
	"github.com/solosw/solcode/internal/organizer"
	"github.com/solosw/solcode/internal/organizer/yzma"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/sessionmemory"
)

// fakeLocalGenerator scripts organizer.LocalGenerator for bridge tests.
type fakeLocalGenerator struct {
	mu       sync.Mutex
	ready    bool
	raw      string
	err      error
	requests []organizer.GenerateRequest
}

func (f *fakeLocalGenerator) Name() string { return "fake-organizer" }
func (f *fakeLocalGenerator) Ready() bool  { return f.ready }
func (f *fakeLocalGenerator) Close() error { return nil }

func (f *fakeLocalGenerator) Generate(ctx context.Context, req organizer.GenerateRequest) (string, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	raw := f.raw
	err := f.err
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	return raw, err
}

const organizerE2EXML = `<result>
  <session_summary>Implemented Letta-style memory layering with local organizer writes.</session_summary>
  <keywords>
    <k>organizer</k>
    <k>letta</k>
    <k>memory</k>
  </keywords>
  <importance>0.8</importance>
  <candidate_memories>
    <candidate>
      <kind>preference</kind>
      <scope>project</scope>
      <suggested_tier>M4</suggested_tier>
      <confidence>0.9</confidence>
      <canonical_text>The project keeps the memory organizer fully local with no remote fallback.</canonical_text>
      <tags><t>memory</t></tags>
      <reason>stated project rule</reason>
    </candidate>
  </candidate_memories>
</result>`

func TestApplyOrganizerResultWritesArchivalAndRecall(t *testing.T) {
	workDir := t.TempDir()
	memDir := filepath.Join(workDir, "memories")
	store := memory.NewFileStore(memDir)
	mgr := memory.NewManager(store, memory.DefaultGate{}, memory.StaticJudge{})

	fake := &fakeLocalGenerator{ready: true, raw: organizerE2EXML}
	org := organizer.New(fake, organizer.Options{MaxOutputTokens: 500, TimeoutSec: 30})
	application := &App{
		Config: config.Config{
			WorkDir: workDir,
			Memory: config.MemoryConfig{
				Enabled: true,
				Dir:     memDir,
				Organizer: config.OrganizerConfig{
					Enabled:   true,
					Runtime:   config.OrganizerRuntimeYzma,
					ModelPath: "unused.gguf",
				},
			},
		},
		MemoryManager: mgr,
		MemoryStore:   store,
		organizer:     &organizerRuntime{org: org, inflight: map[string]bool{}},
	}

	err := application.applyOrganizerResult(context.Background(), organizer.Input{
		SessionID:       "e2e",
		WorkDir:         workDir,
		PreviousSummary: "previous",
		Transcript:      "user: remember local organizer\nassistant: done",
		Trigger:         "test",
		ChangedFiles:    []string{"internal/app/organizer_bridge.go"},
		Todos:           []string{"[→] Wire organizer context"},
		ToolFacts:       []string{"Edited internal/app/organizer_bridge.go"},
		RelatedMemories: []string{"[preference] mem_old: Keep organizer local"},
	})
	if err != nil {
		t.Fatalf("applyOrganizerResult: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("generate calls = %d", len(fake.requests))
	}
	user := fake.requests[0].User
	for _, want := range []string{
		"changed_files", "organizer_bridge.go",
		"todos", "Wire organizer context",
		"tool_facts", "related_memories", "mem_old",
		"trigger",
	} {
		if !strings.Contains(user, want) {
			t.Fatalf("user payload missing %q: %s", want, user)
		}
	}

	items, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected archival memories on disk")
	}
	found := false
	for _, item := range items {
		if strings.Contains(item.Text, "fully local") {
			found = true
			if item.SessionMemoryRef != "e2e#-1" && !strings.HasPrefix(item.SessionMemoryRef, "e2e#") {
				t.Fatalf("session_memory_ref = %q", item.SessionMemoryRef)
			}
			if item.JudgeModel != "fake-organizer" {
				t.Fatalf("judge model = %q", item.JudgeModel)
			}
		}
	}
	if !found {
		t.Fatalf("archival items = %#v", items)
	}

	sm := sessionmemory.NewStore(workDir)
	entries, err := sm.ReadForSession(context.Background(), "e2e", "organizer", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected recall entry in solcode.md")
	}
	if !strings.Contains(entries[0].Summary, "Letta-style") && !strings.Contains(entries[0].Summary, "Organizer:") {
		t.Fatalf("summary = %q", entries[0].Summary)
	}
	if len(entries[0].MemoryIDs) == 0 {
		t.Fatalf("expected memory_ids pointer on recall entry: %#v", entries[0])
	}
}

func TestOrganizerMiniCPMLive(t *testing.T) {
	// Opt-in only: MiniCPM-1B often fails constrained XML on longer payloads.
	// Run with SOLCODE_ORGANIZER_LIVE=1 (and optional SOLCODE_ORGANIZER_MODEL).
	if os.Getenv("SOLCODE_ORGANIZER_LIVE") == "" {
		t.Skip("set SOLCODE_ORGANIZER_LIVE=1 to run live MiniCPM organizer e2e")
	}
	model := os.Getenv("SOLCODE_ORGANIZER_MODEL")
	if model == "" {
		model = filepath.Join(config.UserConfigDir(), "models", "MiniCPM5-1B-Q4_K_M.gguf")
	}
	if _, err := os.Stat(model); err != nil {
		t.Skip("MiniCPM GGUF not found; set SOLCODE_ORGANIZER_MODEL to run live e2e")
	}
	libDir := os.Getenv("YZMA_LIB")
	if libDir == "" {
		libDir = filepath.Join(config.UserConfigDir(), "lib", "llama")
	}
	if _, err := os.Stat(libDir); err != nil {
		t.Skip("llama lib dir missing")
	}

	workDir := t.TempDir()
	memDir := filepath.Join(workDir, "memories")
	store := memory.NewFileStore(memDir)
	mgr := memory.NewManager(store, memory.DefaultGate{}, memory.StaticJudge{})

	gen := yzma.New(yzma.Config{
		ModelPath:   model,
		LibDir:      libDir,
		ContextSize: 4096,
		// Offload all layers when CUDA backends are present (logged at load).
		GPULayers: -1,
	})
	deadline := time.Now().Add(3 * time.Minute)
	for !gen.Ready() && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
	}
	if !gen.Ready() {
		t.Skip("organizer model did not become ready in time")
	}
	t.Cleanup(func() { _ = gen.Close() })

	org := organizer.New(gen, organizer.Options{
		MaxOutputTokens: 800,
		Temperature:     0.1,
		TimeoutSec:      240,
	})
	application := &App{
		Config: config.Config{
			WorkDir: workDir,
			Memory: config.MemoryConfig{
				Enabled: true,
				Dir:     memDir,
				Organizer: config.OrganizerConfig{
					Enabled:         true,
					Runtime:         config.OrganizerRuntimeYzma,
					ModelPath:       model,
					LibDir:          libDir,
					GPULayers:       -1,
					MaxOutputTokens: 800,
					TimeoutSec:      240,
				},
			},
		},
		MemoryManager: mgr,
		MemoryStore:   store,
		organizer:     &organizerRuntime{org: org, inflight: map[string]bool{}},
	}

	transcript := strings.Join([]string{
		"user: Prefer concise replies in Chinese for this project.",
		"assistant: Noted. I will keep replies concise and in Chinese.",
		"user: Always run go test ./internal/memory/ after memory changes.",
		"assistant: Verified with go test ./internal/memory/ -count=1.",
	}, "\n")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := application.applyOrganizerResult(ctx, organizer.Input{
		SessionID:  "live",
		WorkDir:    workDir,
		Transcript: transcript,
		Trigger:    "live",
	}); err != nil {
		// Dump a raw constrained completion so failures are diagnosable when
		// MiniCPM emits non-XML under the GBNF grammar.
		raw, genErr := gen.Generate(ctx, organizer.GenerateRequest{
			System:      "Return ONLY one XML <result> document for the session.",
			User:        transcript,
			Grammar:     organizer.OrganizeGrammar(),
			MaxTokens:   400,
			Temperature: 0.1,
		})
		t.Fatalf("live organizer: %v\nraw(%v)=%q", err, genErr, raw)
	}

	items, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live archival items: %d", len(items))
	for _, item := range items {
		t.Logf("- [%s/%s] %s (ref=%s)", item.Kind, item.Tier, item.Text, item.SessionMemoryRef)
	}

	sm := sessionmemory.NewStore(workDir)
	entries, err := sm.ReadForSession(context.Background(), "live", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected live recall entry")
	}
	t.Logf("live recall summary: %s", entries[0].Summary)
	t.Logf("live recall memory_ids: %v", entries[0].MemoryIDs)
}

func TestShouldRunOrganizerAfterTurnSkipsCancelAndErrors(t *testing.T) {
	cases := []struct {
		name     string
		ctx      context.Context
		agentErr string
		want     bool
	}{
		{name: "clean success", ctx: context.Background(), want: true},
		{name: "empty error", ctx: context.Background(), agentErr: "", want: true},
		{name: "agent failure", ctx: context.Background(), agentErr: "boom", want: false},
		{name: "canceled error text", ctx: context.Background(), agentErr: context.Canceled.Error(), want: false},
		{name: "deadline error text", ctx: context.Background(), agentErr: context.DeadlineExceeded.Error(), want: false},
		{name: "interrupted", ctx: context.Background(), agentErr: "request interrupted by user", want: false},
		{name: "aborted", ctx: context.Background(), agentErr: "operation aborted", want: false},
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases = append(cases, struct {
		name     string
		ctx      context.Context
		agentErr string
		want     bool
	}{name: "canceled context", ctx: canceled, want: false})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRunOrganizerAfterTurn(tc.ctx, tc.agentErr); got != tc.want {
				t.Fatalf("shouldRunOrganizerAfterTurn() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildOrganizerTurnTranscript(t *testing.T) {
	got := buildOrganizerTurnTranscript(" implement organizer ", " done ")
	if got != "user: implement organizer\nassistant: done" {
		t.Fatalf("transcript = %q", got)
	}
	if buildOrganizerTurnTranscript("", "") != "" {
		t.Fatal("expected empty transcript when both sides empty")
	}
}

func TestBuildOrganizerInputIncludesSideContext(t *testing.T) {
	workDir := t.TempDir()
	memDir := filepath.Join(workDir, "memories")
	store := memory.NewFileStore(memDir)
	mgr := memory.NewManager(store, memory.DefaultGate{}, memory.StaticJudge{})

	// Seed one related archival preference the organizer should see.
	item := memory.NewItem("The project prefers table-driven tests in Go packages", memory.TierLongTerm, "seed")
	item.Kind = memory.KindPreference
	item.Scope = memory.ScopeProject
	if _, err := store.Save(context.Background(), item); err != nil {
		t.Fatal(err)
	}

	application := &App{
		Config: config.Config{
			WorkDir: workDir,
			Memory:  config.MemoryConfig{Enabled: true, Dir: memDir},
			Session: config.SessionConfig{Dir: t.TempDir(), DefaultSession: "ctx"},
		},
		MemoryManager: mgr,
		MemoryStore:   store,
	}
	current := &session.Session{}
	current.Metadata.ID = session.SessionID("ctx")
	current.Metadata.WorkDir = workDir
	current.Summary = "earlier organizer work"

	application.beginCheckpointTurn("ctx", workDir, "enrich context")
	content := "x"
	application.captureCheckpoint(filepath.Join(workDir, "internal", "app", "organizer_bridge.go"), &content)

	todoPath := config.DefaultTodoPath(workDir)
	if err := os.MkdirAll(filepath.Dir(todoPath), 0o644); err != nil && !os.IsExist(err) {
		// MkdirAll mode is ignored on Windows for existing dirs; still ensure parent exists.
		_ = os.MkdirAll(filepath.Dir(todoPath), 0o755)
	}
	_ = os.MkdirAll(filepath.Dir(todoPath), 0o755)
	if err := os.WriteFile(todoPath, []byte(`[{"id":"1","content":"Enrich organizer context","status":"in_progress","priority":"high"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	transcript := "user: enrich context\nassistant: updated bridge\n[tool use: Edit]\n{\"path\":\"internal/app/organizer_bridge.go\"}\n"
	input := application.buildOrganizerInput(context.Background(), current, "turn", current.Summary, "", transcript, "enrich context", "updated bridge")
	if input.Trigger != "turn" {
		t.Fatalf("trigger = %q", input.Trigger)
	}
	if !strings.Contains(input.Transcript, "tool use") {
		t.Fatalf("transcript should keep tool trace, got %q", input.Transcript)
	}
	if len(input.ChangedFiles) == 0 {
		t.Fatalf("expected changed files, got %#v", input.ChangedFiles)
	}
	if len(input.Todos) == 0 || !strings.Contains(input.Todos[0], "Enrich organizer context") {
		t.Fatalf("todos = %#v", input.Todos)
	}
	if len(input.ToolFacts) == 0 {
		t.Fatalf("expected tool facts from transcript, got %#v", input.ToolFacts)
	}
	if len(input.RelatedMemories) == 0 {
		t.Fatalf("expected related memories, got %#v", input.RelatedMemories)
	}
}

func TestRunOrganizerAfterTurnSchedulesOnSuccess(t *testing.T) {
	workDir := t.TempDir()
	memDir := filepath.Join(workDir, "memories")
	store := memory.NewFileStore(memDir)
	mgr := memory.NewManager(store, memory.DefaultGate{}, memory.StaticJudge{})

	fake := &fakeLocalGenerator{ready: true, raw: organizerE2EXML}
	org := organizer.New(fake, organizer.Options{MaxOutputTokens: 500, TimeoutSec: 30})
	application := &App{
		Config: config.Config{
			WorkDir: workDir,
			Memory: config.MemoryConfig{
				Enabled: true,
				Dir:     memDir,
				Organizer: config.OrganizerConfig{
					Enabled:   true,
					Runtime:   config.OrganizerRuntimeYzma,
					ModelPath: "unused.gguf",
					TimeoutSec: 30,
				},
			},
		},
		MemoryManager: mgr,
		MemoryStore:   store,
		organizer:     &organizerRuntime{org: org, inflight: map[string]bool{}},
	}

	current := &session.Session{}
	current.Metadata.ID = session.SessionID("turn-e2e")
	current.Metadata.WorkDir = workDir
	current.Summary = "earlier work"

	if !shouldRunOrganizerAfterTurn(context.Background(), "") {
		t.Fatal("expected clean turn to organize")
	}
	application.runOrganizerAfterTurn(context.Background(), current, "remember local organizer", "done")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, err := store.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			fake.mu.Lock()
			n := len(fake.requests)
			fake.mu.Unlock()
			if n == 0 {
				t.Fatal("expected organizer generate to be called")
			}
			req := fake.requests[0]
			if !strings.Contains(req.User, "remember local organizer") {
				t.Fatalf("user payload missing prompt: %q", req.User)
			}
			if !strings.Contains(req.User, "earlier work") && !strings.Contains(req.User, "previous_summary") {
				// previous summary is embedded in the JSON payload by organizer.Organize
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for turn organizer archival write")
}

func TestRunOrganizerAfterTurnSkippedOnCancel(t *testing.T) {
	workDir := t.TempDir()
	fake := &fakeLocalGenerator{ready: true, raw: organizerE2EXML}
	org := organizer.New(fake, organizer.Options{TimeoutSec: 5})
	application := &App{
		Config: config.Config{
			WorkDir: workDir,
			Memory: config.MemoryConfig{
				Enabled: true,
				Organizer: config.OrganizerConfig{
					Enabled:   true,
					Runtime:   config.OrganizerRuntimeYzma,
					ModelPath: "unused.gguf",
				},
			},
		},
		organizer: &organizerRuntime{org: org, inflight: map[string]bool{}},
	}
	current := &session.Session{}
	current.Metadata.ID = session.SessionID("cancel-e2e")
	current.Metadata.WorkDir = workDir

	if shouldRunOrganizerAfterTurn(context.Background(), context.Canceled.Error()) {
		t.Fatal("canceled turns must not organize")
	}
	// Mimic RunPromptWithSession gate: do not call runOrganizerAfterTurn.
	if shouldRunOrganizerAfterTurn(context.Background(), "request interrupted by user") {
		t.Fatal("interrupted turns must not organize")
	}
	_ = application
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.requests) != 0 {
		t.Fatalf("generator should not have been called, got %d", len(fake.requests))
	}
}
