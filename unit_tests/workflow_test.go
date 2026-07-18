package unit_tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/agent"
	"github.com/solosw/solcode/internal/app"
	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/permission"
	"github.com/solosw/solcode/internal/tool"
	"github.com/solosw/solcode/internal/workflow"
)

func TestWorkflowParseYAMLAndValidate(t *testing.T) {
	yamlBody := `
name: demo
description: Demo workflow
tasks:
  - id: a
    description: First
    prompt: Do A with {{args}}
    difficulty: easy
  - id: b
    description: Second
    prompt: Do B
    depends_on: [a]
`
	def, err := workflow.Parse([]byte(yamlBody), "demo.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if def.Name != "demo" {
		t.Fatalf("name = %q", def.Name)
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	params, err := def.ToTaskParams("focus pkg")
	if err != nil {
		t.Fatalf("ToTaskParams: %v", err)
	}
	if len(params.Tasks) != 2 {
		t.Fatalf("tasks = %d", len(params.Tasks))
	}
	if !strings.Contains(params.Tasks[0].Prompt, "focus pkg") {
		t.Fatalf("args not substituted: %q", params.Tasks[0].Prompt)
	}
	if len(params.Tasks[1].DependsOn) != 1 || params.Tasks[1].DependsOn[0] != "a" {
		t.Fatalf("depends_on = %#v", params.Tasks[1].DependsOn)
	}
}

func TestWorkflowRejectsCycle(t *testing.T) {
	def := workflow.Definition{
		Name: "cycle",
		Tasks: []workflow.TaskSpec{
			{ID: "a", Description: "A", Prompt: "A", DependsOn: []string{"b"}},
			{ID: "b", Description: "B", Prompt: "B", DependsOn: []string{"a"}},
		},
	}
	if err := def.Validate(); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestWorkflowRejectsUnknownDependency(t *testing.T) {
	def := workflow.Definition{
		Name: "bad-dep",
		Tasks: []workflow.TaskSpec{
			{ID: "a", Description: "A", Prompt: "A", DependsOn: []string{"missing"}},
		},
	}
	if err := def.Validate(); err == nil {
		t.Fatal("expected unknown dependency error")
	}
}

func TestWorkflowLoadFromDirs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "test-then-review")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
name: test-then-review
description: sample
tasks:
  - id: t
    description: Test
    prompt: run tests
`
	if err := os.WriteFile(filepath.Join(sub, "workflow.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// flat file
	if err := os.WriteFile(filepath.Join(dir, "explore.yaml"), []byte(`
name: explore
description: flat
tasks:
  - id: e
    description: Explore
    prompt: look around
`), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := workflow.LoadFromDirs(dir)
	names := reg.Names()
	if len(names) != 2 {
		t.Fatalf("names = %#v", names)
	}
	if _, ok := reg.Find("test-then-review"); !ok {
		t.Fatal("missing test-then-review")
	}
	if _, ok := reg.Find("explore"); !ok {
		t.Fatal("missing explore")
	}
}

func TestWorkflowNotRegisteredAsModelTool(t *testing.T) {
	cfg := config.Default()
	cfg.WorkDir = t.TempDir()
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	application, err := app.New(cfg)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })

	if application.Tools.Find("Workflow") != nil {
		t.Fatal("Workflow must not be registered as a model tool")
	}
	// Task remains available for the model; workflows reuse it only via App.RunWorkflow.
	if application.Tools.Find(tool.TaskToolName) == nil {
		t.Fatal("expected Task tool to remain registered")
	}
}

func TestRunWorkflowPlanModeBlocked(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "demo.yaml"), []byte(`
name: demo
description: demo
tasks:
  - id: a
    description: A
    prompt: do a
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkDir = dir
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	cfg.Workflows.Paths = []string{wfDir}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	application, err := app.New(cfg)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })

	// Plan mode must fail before spawn.
	application.Permissions.SetMode(permission.ModePlan)
	_, err = application.RunWorkflow(context.Background(), "demo", "")
	if err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected plan mode error, got %v", err)
	}
}

func TestRunWorkflowExecutesTaskGraph(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "demo.yaml"), []byte(`
name: demo
description: demo
tasks:
  - id: a
    description: Easy task
    prompt: Do easy work {{args}}
    difficulty: easy
  - id: b
    description: Hard task
    prompt: Do hard work
    depends_on: [a]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkDir = dir
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	cfg.FastModel = "fast-model"
	cfg.Workflows.Paths = []string{wfDir}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	application, err := app.New(cfg)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })

	runner := &capturingAgentRunner{}
	application.Coordinator = agent.NewCoordinator(runner)

	out, err := application.RunWorkflow(context.Background(), "demo", "pkg/foo")
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if !strings.Contains(out, "[Workflow: demo]") {
		t.Fatalf("missing workflow header: %q", out)
	}
	if len(runner.cfgs) != 2 {
		t.Fatalf("expected 2 sub-agents, got %d", len(runner.cfgs))
	}
	if runner.cfgs[0].Description != "Easy task" {
		t.Fatalf("first task = %#v", runner.cfgs[0])
	}
	if !strings.Contains(runner.cfgs[0].Prompt, "pkg/foo") {
		t.Fatalf("args not applied to prompt: %q", runner.cfgs[0].Prompt)
	}
}

func TestDefaultWorkflowDirs(t *testing.T) {
	work := filepath.Join(t.TempDir(), "proj")
	dirs := config.DefaultWorkflowDirs(work)
	if len(dirs) < 1 {
		t.Fatalf("expected at least user workflows dir, got %#v", dirs)
	}
	joined := strings.Join(dirs, "\n")
	if !strings.Contains(joined, "workflows") {
		t.Fatalf("expected workflows paths, got %#v", dirs)
	}
}

func TestListWorkflows(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "one.yaml"), []byte(`
name: one
description: first
tasks:
  - id: a
    description: A
    prompt: a
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkDir = dir
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	cfg.Workflows.Paths = []string{wfDir}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	application, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })

	defs := application.ListWorkflows()
	if len(defs) != 1 || defs[0].Name != "one" {
		t.Fatalf("ListWorkflows = %#v", defs)
	}
}
