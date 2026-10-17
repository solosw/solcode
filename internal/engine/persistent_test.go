package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
	cpanthropic "github.com/solosw/solcode/internal/anthropic"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

type persistentClient struct {
	requests []cpanthropic.MessageRequest
	respond  func(int, cpanthropic.MessageRequest) (*sdk.Message, error)
}

func (c *persistentClient) Create(_ context.Context, req cpanthropic.MessageRequest) (*sdk.Message, error) {
	index := len(c.requests)
	c.requests = append(c.requests, req)
	return c.respond(index, req)
}
func persistentConfig(dir string) agent.AgentConfig {
	return agent.AgentConfig{ID: "child-resume", ParentID: "main", ParentSessionID: "parent-session", Role: agent.AgentRoleTask, WorkDir: dir, Prompt: "do bounded work", MaxTurns: 4}
}
func persistentScope(cfg agent.AgentConfig) agent.Scope {
	return agent.Scope{ParentID: cfg.ParentID, ParentSessionID: cfg.ParentSessionID, WorkDir: cfg.WorkDir}
}
func persistentWait(t *testing.T, c *agent.Coordinator, id agent.AgentID) agent.AgentResult {
	t.Helper()
	r, err := c.Wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPersistentEngineResumesWithoutReplayingConfirmedTools(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store := agentsession.NewFileStore(storeDir)
	cfg := persistentConfig(dir)
	count := 0
	reg := tool.NewRegistry()
	reg.Register(&backgroundTestTool{name: "Mutate", run: func(context.Context) *tool.ContentBlock {
		// The call must be durable before any real effect.
		saved, err := store.Load(t.Context(), cfg.ID)
		if err != nil {
			t.Error(err)
			return tool.ErrorResult("load failed")
		}
		active, err := saved.Active(agent.Snapshot{})
		if err != nil || len(agent.PendingToolIDs(active.Messages)) != 1 {
			t.Errorf("missing write-ahead checkpoint: %v", err)
		}
		count++
		return tool.Result("confirmed mutation")
	}})
	client := &persistentClient{respond: func(index int, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
		m := backgroundMessage(t)
		if index == 0 {
			m = backgroundMessage(t, backgroundCall("mutate-once", "Mutate", map[string]any{}))
		}
		m.Usage = sdk.Usage{InputTokens: 11, OutputTokens: 2, CacheReadInputTokens: 7}
		return m, nil
	}}
	eng := NewEngine(Config{Client: client, Tools: reg, ModelName: "current-model", MaxTurns: 4})
	c := agent.NewCoordinator(eng, store)
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	first := persistentWait(t, c, cfg.ID)
	if first.Error != "" {
		t.Fatal(first.Error)
	}
	secondClient := &persistentClient{respond: func(_ int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		texts := backgroundResultTexts(req.Messages, "mutate-once")
		if len(texts) != 1 || texts[0] != "confirmed mutation" {
			t.Errorf("restored results: %v", texts)
		}
		return backgroundMessage(t), nil
	}}
	resumed := agent.NewCoordinator(NewEngine(Config{Client: secondClient, Tools: reg, ModelName: "new-model", MaxTurns: 1}), agentsession.NewFileStore(storeDir))
	cfg.Prompt = "follow up"
	cfg.MaxTurns = 1
	if _, err := resumed.Resume(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	result := persistentWait(t, resumed, cfg.ID)
	if result.Error != "" || count != 1 {
		t.Fatalf("result %+v, mutations=%d", result, count)
	}
	saved, err := resumed.Get(t.Context(), cfg.ID, persistentScope(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Runs) != 2 || saved.Runs[0].Usage.InputTokens != 22 || saved.Runs[0].Usage.CacheReadInputTokens != 14 {
		t.Fatalf("runs: %+v", saved.Runs)
	}
	if saved.Runs[0].Model != "current-model" || saved.Runs[1].Model != "new-model" {
		t.Fatalf("model configuration not re-evaluated: %+v", saved.Runs)
	}
	if client.requests[0].System != secondClient.requests[0].System {
		t.Fatal("resume changed stable system prefix")
	}
}

func TestPersistentProcessCrashAfterMutationDoesNotReplay(t *testing.T) {
	if dir := os.Getenv("SOLCODE_CRASH_TEST_DIR"); dir != "" {
		store := agentsession.NewFileStore(dir)
		cfg := persistentConfig(dir)
		reg := tool.NewRegistry()
		reg.Register(&backgroundTestTool{name: "Mutate", run: func(context.Context) *tool.ContentBlock {
			if err := os.WriteFile(filepath.Join(dir, "effect.txt"), []byte("committed once"), 0o600); err != nil {
				os.Exit(3)
			}
			os.Exit(0) // Kill the worker after the effect but before tool-result Save.
			return nil
		}})
		client := &persistentClient{respond: func(_ int, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
			return backgroundMessage(t, backgroundCall("unknown-effect", "Mutate", map[string]any{})), nil
		}}
		c := agent.NewCoordinator(NewEngine(Config{Client: client, Tools: reg, ModelName: "test", MaxTurns: 2}), store)
		if _, err := c.Spawn(context.Background(), cfg); err != nil {
			os.Exit(4)
		}
		_, _ = c.Wait(context.Background(), cfg.ID)
		os.Exit(5)
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPersistentProcessCrashAfterMutationDoesNotReplay$")
	cmd.Env = append(os.Environ(), "SOLCODE_CRASH_TEST_DIR="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "effect.txt")); err != nil || string(data) != "committed once" {
		t.Fatalf("effect missing: %q %v", data, err)
	}
	client := &persistentClient{respond: func(_ int, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
		t.Error("unsafe resume contacted model")
		return backgroundMessage(t), nil
	}}
	c := agent.NewCoordinator(NewEngine(Config{Client: client, ModelName: "test"}), agentsession.NewFileStore(dir))
	cfg := persistentConfig(dir)
	if _, err := c.Resume(t.Context(), cfg); !errors.Is(err, agent.ErrUncertain) {
		t.Fatalf("crashed mutation auto-resumed: %v", err)
	}
	saved, err := c.Get(t.Context(), cfg.ID, persistentScope(cfg))
	if err != nil || saved.Status.State != agent.AgentInterrupted {
		t.Fatalf("crash status: %+v %v", saved, err)
	}
}

func TestPersistentModelRetryAppendsPromptOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	store := agentsession.NewFileStore(dir)
	client := &persistentClient{respond: func(index int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		if index == 0 {
			return nil, errors.New("temporary model error")
		}
		count := 0
		for _, message := range req.Messages {
			for _, block := range message.Content {
				if block.OfText != nil && block.OfText.Text == "one logical prompt" {
					count++
				}
			}
		}
		if count != 1 {
			t.Errorf("prompt duplicated on retry: %d", count)
		}
		return backgroundMessage(t), nil
	}}
	c := agent.NewCoordinator(NewEngine(Config{Client: client, ModelName: "test", MaxTurns: 2}), store)
	task := tool.NewTaskTool(c)
	result, err := task.Invoke(t.Context(), &tool.UseContext{SessionID: "parent", AgentID: "main", WorkDir: dir, TaskRetryDelay: time.Millisecond}, json.RawMessage(`{"description":"retry","prompt":"one logical prompt"}`))
	if err != nil || result.IsError {
		t.Fatalf("retry: %+v %v", result, err)
	}
	statuses := c.List()
	if len(statuses) != 1 || len(client.requests) != 2 {
		t.Fatal("retry spawned extra agents or requests")
	}
	saved, err := store.Load(t.Context(), statuses[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Runs) != 2 || saved.Runs[0].ExecutionID != saved.Runs[1].ExecutionID {
		t.Fatal("retry correlation lost")
	}
}

func TestPersistentCheckpointFailureStopsBeforeTool(t *testing.T) {
	count := 0
	reg := tool.NewRegistry()
	reg.Register(&backgroundTestTool{name: "Mutate", run: func(context.Context) *tool.ContentBlock { count++; return tool.Result("bad") }})
	client := &persistentClient{respond: func(_ int, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
		return backgroundMessage(t, backgroundCall("blocked-save", "Mutate", map[string]any{})), nil
	}}
	eng := NewEngine(Config{Client: client, Tools: reg, ModelName: "test", MaxTurns: 2})
	result := eng.RunSession(t.Context(), agent.SessionRequest{Config: persistentConfig(t.TempDir()), Save: func(cp agent.Checkpoint) error {
		if cp.Stage == "response" {
			return errors.New("disk full")
		}
		return nil
	}})
	if count != 0 || !result.NoRetry || !strings.Contains(result.Error, "disk full") {
		t.Fatalf("result=%+v mutations=%d", result, count)
	}
}

func TestPersistentCancellationSavesPartialResultsAndRequiresReconciliation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := agentsession.NewFileStore(t.TempDir())
	cfg := persistentConfig(t.TempDir())
	count := 0
	reg := tool.NewRegistry()
	reg.Register(&backgroundTestTool{name: "Mutate", run: func(context.Context) *tool.ContentBlock {
		count++
		cancel()
		return tool.Result("first result committed")
	}})
	client := &persistentClient{respond: func(_ int, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
		return backgroundMessage(t, backgroundCall("first", "Mutate", map[string]any{}), backgroundCall("second", "Mutate", map[string]any{})), nil
	}}
	c := agent.NewCoordinator(NewEngine(Config{Client: client, Tools: reg, ModelName: "test", MaxTurns: 3}), store)
	if _, err := c.Spawn(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	result := persistentWait(t, c, cfg.ID)
	if result.Error == "" || !result.NoRetry || count != 1 {
		t.Fatalf("result=%+v mutations=%d", result, count)
	}
	saved, err := store.Load(t.Context(), cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := saved.Active(agent.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	pending := agent.PendingToolIDs(snapshot.Messages)
	if len(pending) != 1 || pending[0] != "second" || len(backgroundResultTexts(snapshot.Messages, "first")) != 1 {
		t.Fatalf("partial results lost: pending=%v", pending)
	}
	if _, err := c.Resume(t.Context(), cfg); !errors.Is(err, agent.ErrUncertain) {
		t.Fatalf("unsafe resume: %v", err)
	}
}

func TestPersistentResumeUsesCurrentPermissions(t *testing.T) {
	store := agentsession.NewFileStore(t.TempDir())
	cfg := persistentConfig(t.TempDir())
	count := 0
	reg := tool.NewRegistry()
	reg.Register(&backgroundTestTool{name: "Mutate", run: func(context.Context) *tool.ContentBlock { count++; return tool.Result("changed") }})
	permissions := permission.NewService(permission.ModeUltra)
	client := &persistentClient{respond: func(index int, _ cpanthropic.MessageRequest) (*sdk.Message, error) {
		if index%2 == 0 {
			return backgroundMessage(t, backgroundCall(agent.NewID("call"), "Mutate", map[string]any{})), nil
		}
		return backgroundMessage(t), nil
	}}
	c := agent.NewCoordinator(NewEngine(Config{Client: client, Tools: reg, Permissions: permissions, ModelName: "test", MaxTurns: 4}), store)
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if r := persistentWait(t, c, cfg.ID); r.Error != "" {
		t.Fatal(r.Error)
	}
	permissions.DenyTool("Mutate")
	if _, err := c.Resume(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if r := persistentWait(t, c, cfg.ID); r.Error != "" {
		t.Fatal(r.Error)
	}
	if count != 1 {
		t.Fatalf("revoked tool executed: %d", count)
	}
	saved, err := store.Load(t.Context(), cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(saved.Messages)
	if !strings.Contains(string(data), "tool denied for session") {
		t.Fatal("permission denial missing from history")
	}
}

func TestWorkingSetSnapshotRestoresMetadataNotCachedContent(t *testing.T) {
	w := NewWorkingSet()
	w.SetGoal("old task")
	w.ObserveFile("a.go", []byte("private content"), true, false)
	w.AddFact("verified fact")
	w.AddOpen("next step")
	snapshot := w.Snapshot()
	restored := RestoreWorkingSet(snapshot)
	if !restored.HasContext() {
		t.Fatal("working set lost")
	}
	if _, ok := restored.Lookup("a.go"); ok {
		t.Fatal("cached contents were duplicated")
	}
	if strings.Contains(string(snapshot), "private content") || !strings.Contains(restored.InjectionText(), "verified fact") {
		t.Fatal("metadata persistence failed")
	}
}

func TestPersistentCompactionKeepsArchiveAndAvoidsMainCallback(t *testing.T) {
	store := agentsession.NewFileStore(t.TempDir())
	cfg := persistentConfig(t.TempDir())
	cfg.MaxTurns = 1
	parent := make([]sdk.MessageParam, 0)
	for i := 0; i < 25; i++ {
		parent = append(parent, sdk.NewUserMessage(sdk.NewTextBlock("old request")), sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("older analysis ", 2000))))
	}
	cfg.ForkContext = "full"
	cfg.ParentContext = agent.Snapshot{Messages: parent}
	mainCalled := false
	client := &persistentClient{respond: func(_ int, req cpanthropic.MessageRequest) (*sdk.Message, error) {
		if session.ApproxTokensFromMessages(req.Messages) >= session.ApproxTokensFromMessages(parent) {
			t.Error("context did not shrink")
		}
		return backgroundMessage(t), nil
	}}
	eng := NewEngine(Config{Client: client, ModelName: "test", MaxTurns: 1, MaxContextTokens: 32_000, CompactMessages: func(context.Context, []sdk.MessageParam) ([]sdk.MessageParam, error) {
		mainCalled = true
		return nil, errors.New("must not use main compactor")
	}})
	c := agent.NewCoordinator(eng, store)
	if _, err := c.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	result := persistentWait(t, c, cfg.ID)
	if result.Error != "" {
		t.Fatal(result.Error)
	}
	if mainCalled || len(client.requests) != 1 {
		t.Fatalf("mainCalled=%v requests=%d", mainCalled, len(client.requests))
	}
	history, err := c.History(t.Context(), cfg.ID, persistentScope(cfg))
	if err != nil || len(history.Messages) < len(parent) {
		t.Fatalf("archive lost: %v", err)
	}
}
