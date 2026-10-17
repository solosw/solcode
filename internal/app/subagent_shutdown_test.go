package app

import (
	"context"
	"testing"

	"github.com/solosw/solcode/internal/agent"
)

type shutdownChildRunner struct {
	started chan struct{}
	stopped chan struct{}
}

func (r shutdownChildRunner) Run(ctx context.Context, _ agent.AgentConfig) agent.AgentResult {
	close(r.started)
	<-ctx.Done()
	close(r.stopped)
	return agent.AgentResult{Error: ctx.Err().Error(), NoRetry: true}
}

func TestAppCloseStopsDetachedChildren(t *testing.T) {
	runner := shutdownChildRunner{started: make(chan struct{}), stopped: make(chan struct{})}
	coordinator := agent.NewCoordinator(runner)
	t.Cleanup(coordinator.Close)
	cfg := agent.AgentConfig{ID: "detached-child", Detached: true, Prompt: "work", WorkDir: t.TempDir()}
	if _, err := coordinator.Spawn(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	application := &App{Coordinator: coordinator}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.stopped:
	default:
		t.Fatal("App.Close returned before the child exited")
	}
	result, err := coordinator.Wait(t.Context(), cfg.ID)
	if err != nil || result.Error != context.Canceled.Error() {
		t.Fatalf("shutdown result: %+v %v", result, err)
	}
}
