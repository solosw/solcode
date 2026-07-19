package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
	"github.com/solosw/solcode/internal/engine"
	"github.com/solosw/solcode/internal/skill"
	"github.com/solosw/solcode/internal/tool"
)

func TestLoadFromDirsSupportsMarkdownFilesAndSkillDirectories(t *testing.T) {
	root := t.TempDir()
	reviewDir := filepath.Join(root, "review")
	if err := os.MkdirAll(reviewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Structured code review workflow\n---\n# review\nDo the review.\n"
	if err := os.WriteFile(filepath.Join(reviewDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := skill.LoadFromDirs(root)
	if _, ok := registry.Find("review"); !ok {
		t.Fatal("expected review skill")
	}
	if _, ok := registry.Find("notes"); !ok {
		t.Fatal("expected notes skill")
	}
	reviewDef, _ := registry.Find("review")
	if got, want := filepath.Base(reviewDef.Path), "SKILL.md"; got != want {
		t.Fatalf("review path base = %q, want %q", got, want)
	}
	if !strings.Contains(reviewDef.Description, "Structured code review") {
		t.Fatalf("review description = %q, want frontmatter description", reviewDef.Description)
	}
}

func TestLoadFromDirsTreatsSkillRootDirectly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# local skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := skill.LoadFromDirs(root)
	def, ok := registry.Find(filepath.Base(root))
	if !ok {
		// name is directory base of temp dir
		all := registry.All()
		if len(all) != 1 {
			t.Fatalf("expected 1 skill, got %d", len(all))
		}
		def = all[0]
	}
	if def.Path != filepath.Join(root, "SKILL.md") {
		t.Fatalf("path = %q, want SKILL.md under root", def.Path)
	}
}

func TestSkillToolReturnsBodyResourcesAndStripsFrontmatter(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "review")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Review changes carefully\nallowed-tools: Bash Read\n---\n# Review\n\nConsult references/checklist.md and run scripts/scan.py.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "scripts", "scan.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "references", "checklist.md"), []byte("# checklist\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "assets", "template.txt"), []byte("tmpl\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := skill.LoadFromDirs(root)
	skillTool := tool.NewSkillTool(registry)
	result, err := skillTool.Invoke(context.Background(), &tool.UseContext{WorkDir: root}, json.RawMessage(`{"skill":"review","args":"for auth changes"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	text := result.Text
	if !strings.Contains(text, "Args: for auth changes") {
		t.Fatalf("missing args: %q", text)
	}
	if !strings.Contains(text, "## Instructions") {
		t.Fatalf("missing instructions section: %q", text)
	}
	if !strings.Contains(text, "Consult references/checklist.md") {
		t.Fatalf("missing body: %q", text)
	}
	if strings.Contains(text, "name: review") {
		t.Fatalf("frontmatter should be stripped from body: %q", text)
	}
	if !strings.Contains(text, "Root: ") {
		t.Fatalf("missing root: %q", text)
	}
	if !strings.Contains(text, "scripts/scan.py") {
		t.Fatalf("missing script listing: %q", text)
	}
	if !strings.Contains(text, "references/checklist.md") {
		t.Fatalf("missing reference listing: %q", text)
	}
	if !strings.Contains(text, "assets/template.txt") {
		t.Fatalf("missing asset listing: %q", text)
	}
	if !strings.Contains(text, "Allowed-tools: Bash Read") {
		t.Fatalf("missing allowed-tools: %q", text)
	}
}

func TestParseDocumentFrontmatter(t *testing.T) {
	meta, body, ok := skill.ParseDocument("---\nname: pdf\ndescription: Handle PDFs\n---\n\n# Body\nDo work.\n")
	if !ok {
		t.Fatal("expected frontmatter")
	}
	if meta.Name != "pdf" || meta.Description != "Handle PDFs" {
		t.Fatalf("meta = %+v", meta)
	}
	if !strings.Contains(body, "# Body") {
		t.Fatalf("body = %q", body)
	}
}

func TestSkillsPromptIncludesDescriptions(t *testing.T) {
	builder := engine.ContextBuilder{
		Skills: []engine.SkillInfo{
			{Name: "review", Description: "Structured code review"},
			{Name: "commit", Description: "Write a commit message"},
		},
	}
	req := builder.Build(engine.BuildRequest{Model: "test", MaxTokens: 16})
	if !strings.Contains(req.System, "review: Structured code review") {
		t.Fatalf("system prompt missing skill descriptions: %q", req.System)
	}
	if !strings.Contains(req.System, "scripts/") {
		t.Fatalf("system prompt should mention package layout: %q", req.System)
	}
}

func TestDefaultSkillDirs(t *testing.T) {
	workDir := filepath.Join("tmp", "project")
	dirs := config.DefaultSkillDirs(workDir)
	want := []string{
		filepath.Join(config.UserConfigDir(), "skills"),
		filepath.Join(config.ProjectConfigDir(workDir), "skills"),
		filepath.Join(config.UserConfigDir(), "my-skill"),
		filepath.Join(config.ProjectConfigDir(workDir), "my-skill"),
	}
	if len(dirs) < 2 {
		t.Fatalf("DefaultSkillDirs() = %v", dirs)
	}
	for i, path := range want {
		if i >= len(dirs) {
			break
		}
		if dirs[i] != path {
			// Order is user skills, project skills, then legacy paths.
			found := false
			for _, d := range dirs {
				if d == path {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("DefaultSkillDirs missing %q in %v", path, dirs)
			}
		}
	}
}
