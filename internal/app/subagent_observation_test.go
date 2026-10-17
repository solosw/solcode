package app

import (
	"context"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/agentsession"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/session"
	"github.com/solosw/solcode/internal/tool"
)

type observationResumeRunner struct{}

func (observationResumeRunner) Run(context.Context, agent.AgentConfig) agent.AgentResult {
	return agent.AgentResult{Output: "done"}
}

func TestChildReadObservationResolvesDurableParentReference(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	app := &App{Config: config.Config{WorkDir: workDir, Session: config.SessionConfig{Dir: dir, DefaultSession: "parent"}}}
	parent := session.NewFileObservationStore(session.ObservationStoreDir(dir, "parent"))
	if _, err := parent.Save("inherited-observation", "parent original output"); err != nil {
		t.Fatal(err)
	}
	child := &agent.Record{Config: agent.AgentConfig{ID: "child", ParentSessionID: "parent", WorkDir: workDir}, Status: agent.AgentStatus{ID: "child"}}
	if err := agentsession.NewFileStore(dir).Save(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	got, err := app.ReadObservation(t.Context(), tool.ObservationReadRequest{ID: "inherited-observation", SessionID: "child", WorkDir: workDir})
	if err != nil || got != "parent original output" {
		t.Fatalf("inherited reference: %q %v", got, err)
	}
	// Resume a legacy record from another parent session. Its inherited
	// observation must still resolve against the original parent's store.
	coordinator := agent.NewCoordinator(observationResumeRunner{}, agentsession.NewFileStore(dir))
	if _, err := coordinator.Resume(t.Context(), agent.AgentConfig{ID: "child", ParentID: "another-main", ParentSessionID: "another-session", WorkDir: workDir, Prompt: "continue"}); err != nil {
		t.Fatal(err)
	}
	if result, err := coordinator.Wait(t.Context(), "child"); err != nil || result.Error != "" {
		t.Fatalf("resume: %+v %v", result, err)
	}
	otherParent := session.NewFileObservationStore(session.ObservationStoreDir(dir, "another-session"))
	if _, err := otherParent.Save("inherited-observation", "wrong parent output"); err != nil {
		t.Fatal(err)
	}
	got, err = app.ReadObservation(t.Context(), tool.ObservationReadRequest{ID: "inherited-observation", SessionID: "child", WorkDir: workDir})
	if err != nil || got != "parent original output" {
		t.Fatalf("cross-session inherited reference: %q %v", got, err)
	}
	if _, err := app.ReadObservation(t.Context(), tool.ObservationReadRequest{ID: "inherited-observation", SessionID: "child", WorkDir: t.TempDir()}); err == nil {
		t.Fatal("foreign workdir resolved parent observation")
	}
}
