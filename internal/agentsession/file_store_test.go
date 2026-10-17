package agentsession_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
	"github.com/solosw/solcode/internal/session"
)

type historyRunner struct{ snapshots []agent.Snapshot }

func (r *historyRunner) Run(context.Context, agent.AgentConfig) agent.AgentResult {
	panic("must use persistent runner")
}
func (r *historyRunner) RunSession(_ context.Context, req agent.SessionRequest) agent.AgentResult {
	r.snapshots = append(r.snapshots, req.Snapshot)
	messages := append(req.Snapshot.Messages, sdk.NewUserMessage(sdk.NewTextBlock(req.Config.Prompt)), sdk.NewAssistantMessage(sdk.NewTextBlock("done")))
	err := req.Save(agent.Checkpoint{Snapshot: agent.Snapshot{Messages: messages}, UsageKnown: true, Usage: agent.TokenUsage{InputTokens: 10, CacheReadInputTokens: 7}, Stage: "response"})
	if err != nil {
		return agent.AgentResult{Error: err.Error(), NoRetry: true}
	}
	return agent.AgentResult{Output: "done"}
}

func config() agent.AgentConfig {
	return agent.AgentConfig{ID: "persistent-child", ParentID: "main", ParentSessionID: "session-one", Role: agent.AgentRoleTask, WorkDir: "/project", Prompt: "first", AllowedTools: []string{"View"}}
}
func scope(cfg agent.AgentConfig) agent.Scope {
	return agent.Scope{ParentID: cfg.ParentID, ParentSessionID: cfg.ParentSessionID, WorkDir: cfg.WorkDir}
}
func wait(t *testing.T, c *agent.Coordinator, id agent.AgentID) agent.AgentResult {
	t.Helper()
	r, err := c.Wait(t.Context(), id)
	if err != nil || r.Error != "" {
		t.Fatalf("wait: %+v %v", r, err)
	}
	return r
}

func TestHistorySurvivesCoordinatorAndStoreRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := config()
	runner := &historyRunner{}
	first := agent.NewCoordinator(runner, agentsession.NewFileStore(dir))
	if _, err := first.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	initial := wait(t, first, cfg.ID)
	secondRunner := &historyRunner{}
	second := agent.NewCoordinator(secondRunner, agentsession.NewFileStore(dir))
	before, err := second.Get(t.Context(), cfg.ID, scope(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Messages) != 2 || before.Result.Output != "done" || before.Status.State != agent.AgentIdle {
		t.Fatalf("record: %+v", before)
	}
	cfg.Prompt = "continue"
	cfg.ParentID = "another-main"
	cfg.ParentSessionID = "session-two"
	cfg.AllowedTools = nil
	if _, err := second.Resume(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	result := wait(t, second, cfg.ID)
	if result.AgentID != initial.AgentID || result.RunID == initial.RunID || result.RunID == "" {
		t.Fatalf("ids: %+v %+v", initial, result)
	}
	if len(secondRunner.snapshots) != 1 || len(secondRunner.snapshots[0].Messages) != 2 {
		t.Fatalf("missing restored history")
	}
	saved, err := second.Get(t.Context(), cfg.ID, scope(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Runs) != 2 || len(saved.Messages) != 4 || len(saved.Config.AllowedTools) != 1 || saved.Config.AllowedTools[0] != "View" {
		t.Fatalf("saved: %+v", saved)
	}
	if len(saved.MessageTimestamps) != 4 || !saved.MessageTimestamps[0].Equal(before.MessageTimestamps[0]) {
		t.Fatal("resuming reset historical message timestamps")
	}
	if saved.ForkParentSessionID != "session-one" || saved.Runs[0].ParentSessionID != "session-one" || saved.Runs[1].ParentSessionID != "session-two" || saved.Runs[1].ParentID != "another-main" {
		t.Fatalf("cross-session provenance lost: %+v", saved)
	}
	if saved.Runs[0].Usage.CacheReadInputTokens != 7 {
		t.Fatalf("usage not saved")
	}
	if _, err := second.Get(t.Context(), cfg.ID, agent.Scope{ParentID: "other"}); err == nil {
		t.Fatal("foreign scope allowed")
	}
	if _, err := second.Resume(t.Context(), agent.AgentConfig{ID: "missing"}); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := second.Archive(t.Context(), cfg.ID, scope(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Resume(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("archive resume: %v", err)
	}
}

func TestExclusiveLockAndProcessExitRecovery(t *testing.T) {
	if dir := os.Getenv("SOLCODE_LOCK_CHILD_DIR"); dir != "" {
		_, err := agentsession.NewFileStore(dir).Acquire(context.Background(), "crash-child")
		if err != nil {
			os.Exit(3)
		}
		os.Exit(0) // Deliberately do not release: simulate abrupt process termination.
	}
	dir := t.TempDir()
	store := agentsession.NewFileStore(dir)
	release, err := store.Acquire(t.Context(), "locked-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentsession.NewFileStore(dir).Acquire(t.Context(), "locked-child"); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("second owner: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestExclusiveLockAndProcessExitRecovery$")
	cmd.Env = append(os.Environ(), "SOLCODE_LOCK_CHILD_DIR="+dir)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, data)
	}
	release, err = store.Acquire(t.Context(), "crash-child")
	if err != nil {
		t.Fatalf("lock survived process death: %v", err)
	}
	defer release()
}

func TestForkSnapshotsSharedAndChildStoresOnlyDelta(t *testing.T) {
	dir := t.TempDir()
	store := agentsession.NewFileStore(dir)
	parent := agent.Snapshot{Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("shared parent history"))}, WorkingSet: json.RawMessage(`{"Goal":"goal"}`)}
	cfg := config()
	cfg.ForkContext = "full"
	cfg.ParentContext = parent
	c := agent.NewCoordinator(&historyRunner{}, store)
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	wait(t, c, cfg.ID)
	one, err := c.Get(t.Context(), cfg.ID, scope(cfg))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID = "second-child"
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	wait(t, c, cfg.ID)
	two, err := c.Get(t.Context(), cfg.ID, scope(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if one.ForkID == "" || one.ForkID != two.ForkID {
		t.Fatalf("fork not shared: %q %q", one.ForkID, two.ForkID)
	}
	if len(one.Messages) != 2 || len(two.Messages) != 2 {
		t.Fatal("inherited messages duplicated")
	}
	history, err := c.History(t.Context(), cfg.ID, scope(cfg))
	if err != nil || len(history.Messages) != 3 {
		t.Fatalf("history: %+v %v", history, err)
	}
	// Main sessions never list the independent child namespace.
	metas, err := session.NewFileStore(dir).List(t.Context())
	if err != nil || len(metas) != 0 {
		t.Fatalf("main list: %+v %v", metas, err)
	}
}

type slowCancelRunner struct {
	started chan struct{}
	stop    chan struct{}
}

func (r *slowCancelRunner) Run(context.Context, agent.AgentConfig) agent.AgentResult {
	close(r.started)
	<-r.stop
	return agent.AgentResult{Output: "stopped"}
}

func TestResumeStaysBusyUntilCancelledRunnerActuallyExits(t *testing.T) {
	dir := t.TempDir()
	runner := &slowCancelRunner{started: make(chan struct{}), stop: make(chan struct{})}
	cfg := config()
	c := agent.NewCoordinator(runner, agentsession.NewFileStore(dir))
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := c.Cancel(cfg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resume(t.Context(), cfg); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("cancel released live runner: %v", err)
	}
	other := agent.NewCoordinator(&historyRunner{}, agentsession.NewFileStore(dir))
	if _, err := other.Resume(t.Context(), cfg); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("other coordinator bypassed lock: %v", err)
	}
	close(runner.stop)
	result, err := c.Wait(t.Context(), cfg.ID)
	if err != nil || !result.NoRetry || result.Error == "" {
		t.Fatalf("cancel: %+v %v", result, err)
	}
	if _, err := other.Resume(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	wait(t, other, cfg.ID)
}

func TestOverlappingForkSnapshotsShareIndividualMessages(t *testing.T) {
	dir := t.TempDir()
	store := agentsession.NewFileStore(dir)
	first := agent.Snapshot{Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("shared one")), sdk.NewAssistantMessage(sdk.NewTextBlock("shared two"))}}
	firstID, err := store.SaveFork(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Messages = append(append([]sdk.MessageParam(nil), first.Messages...), sdk.NewUserMessage(sdk.NewTextBlock("new turn")))
	secondID, err := store.SaveFork(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatal("distinct snapshots have identical ids")
	}
	files, err := os.ReadDir(filepath.Join(dir, "subagents", "forks", "messages"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 unique message objects, got %d", len(files))
	}
	loaded, err := agentsession.NewFileStore(dir).LoadFork(t.Context(), secondID)
	if err != nil || len(loaded.Messages) != 3 {
		t.Fatalf("snapshot: %+v %v", loaded, err)
	}
}

func TestCrashWithPendingToolsRefusesAutomaticResume(t *testing.T) {
	dir := t.TempDir()
	store := agentsession.NewFileStore(dir)
	cfg := config()
	use := sdk.NewAssistantMessage(sdk.NewToolUseBlock("tool-unknown", map[string]any{"command": "mutate"}, "Bash"))
	r := &agent.Record{Config: cfg, Status: agent.AgentStatus{ID: cfg.ID, RunID: "old-run", State: agent.AgentRunning}, Messages: []sdk.MessageParam{use}, Context: []int{0}, Runs: []agent.RunRecord{{ID: "old-run", State: agent.AgentRunning}}}
	if err := store.Save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	runner := &historyRunner{}
	c := agent.NewCoordinator(runner, agentsession.NewFileStore(dir))
	if _, err := c.Resume(t.Context(), cfg); !errors.Is(err, agent.ErrUncertain) {
		t.Fatalf("resume: %v", err)
	}
	if len(runner.snapshots) != 0 {
		t.Fatal("uncertain tool call triggered a runner")
	}
	// Acknowledgement is explicit; it appends unknown results, never invokes tools.
	cfg.AcknowledgeInterruption = true
	if _, err := c.Resume(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	wait(t, c, cfg.ID)
	if len(agent.PendingToolIDs(runner.snapshots[0].Messages)) != 0 {
		t.Fatal("unbalanced recovered exchange")
	}
	saved, err := c.Get(t.Context(), cfg.ID, scope(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Runs[0].State != agent.AgentInterrupted {
		t.Fatal("interrupted attempt lost")
	}
}
