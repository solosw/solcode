package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/codeplus-agent/internal/config"
	"github.com/solosw/codeplus-agent/internal/skill"
	"github.com/solosw/codeplus-agent/internal/tool"
)

func TestLoadFromDirsSupportsMarkdownFilesAndSkillDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "verify.md"), []byte("# verify\n"), 0o644); err != nil {
		t.Fatalf("write markdown skill: %v", err)
	}
	reviewDir := filepath.Join(root, "review")
	if err := os.MkdirAll(filepath.Join(reviewDir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir review skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reviewDir, "SKILL.md"), []byte("# review\n"), 0o644); err != nil {
		t.Fatalf("write directory skill: %v", err)
	}

	registry := skill.LoadFromDirs(root)
	defs := registry.All()
	if len(defs) != 2 {
		t.Fatalf("expected 2 loaded skills, got %d", len(defs))
	}

	verifyDef, ok := registry.Find("verify")
	if !ok {
		t.Fatal("expected verify markdown skill to load")
	}
	if got, want := filepath.Base(verifyDef.Path), "verify.md"; got != want {
		t.Fatalf("verify skill path = %q, want %q", got, want)
	}

	reviewDef, ok := registry.Find("review")
	if !ok {
		t.Fatal("expected review directory skill to load")
	}
	if got, want := filepath.Base(reviewDef.Path), "SKILL.md"; got != want {
		t.Fatalf("review skill path = %q, want %q", got, want)
	}
	if got, want := filepath.Base(reviewDef.Source), "review"; got != want {
		t.Fatalf("review skill source = %q, want %q", got, want)
	}
}

func TestLoadFromDirsSupportsDirectSkillDirectoryPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# local skill\n"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	registry := skill.LoadFromDirs(root)
	def, ok := registry.Find(filepath.Base(root))
	if !ok {
		t.Fatalf("expected skill named after directory %q", filepath.Base(root))
	}
	if def.Path != filepath.Join(root, "SKILL.md") {
		t.Fatalf("unexpected skill path %q", def.Path)
	}
}

func TestSkillToolReadsDirectorySkillFile(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "review")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := "---\nname: review\n---\n\nUse the checklist.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	registry := skill.LoadFromDirs(root)
	skillTool := tool.NewSkillTool(registry)
	result, err := skillTool.Invoke(context.Background(), &tool.UseContext{WorkDir: root}, json.RawMessage(`{"skill":"review","args":"for auth changes"}`))
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Invoke() returned tool error: %s", result.Text)
	}
	for _, want := range []string{"[Skill: review]", "Args: for auth changes", "Use the checklist."} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("expected result to contain %q, got %q", want, result.Text)
		}
	}
}

func TestDefaultSkillDirsKeepLegacyCompatibility(t *testing.T) {
	workDir := t.TempDir()
	dirs := config.DefaultSkillDirs(workDir)
	want := []string{
		filepath.Join(config.UserConfigDir(), "skills"),
		filepath.Join(config.ProjectConfigDir(workDir), "skills"),
		filepath.Join(config.UserConfigDir(), "my-skill"),
		filepath.Join(config.ProjectConfigDir(workDir), "my-skill"),
	}
	if len(dirs) != len(want) {
		t.Fatalf("DefaultSkillDirs() = %#v", dirs)
	}
	for i, path := range want {
		if dirs[i] != path {
			t.Fatalf("DefaultSkillDirs()[%d] = %q, want %q", i, dirs[i], path)
		}
	}
}
