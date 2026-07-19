package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/tool"
)

func TestResolvePathPrefersSkillRootForBundledResources(t *testing.T) {
	workDir := t.TempDir()
	skillRoot := t.TempDir()
	resource := filepath.Join(skillRoot, "references", "guide.md")
	if err := os.MkdirAll(filepath.Dir(resource), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resource, []byte("skill guide"), 0o644); err != nil {
		t.Fatal(err)
	}

	uctx := &tool.UseContext{WorkDir: workDir, SkillRoots: []string{skillRoot}}
	if got := tool.ResolvePath(uctx, "references/guide.md"); got != resource {
		t.Fatalf("ResolvePath() = %q, want %q", got, resource)
	}
}

func TestViewReadsUserDirectorySkillReferenceByRelativePath(t *testing.T) {
	workDir := t.TempDir()
	skillRoot := t.TempDir() // deliberately outside WorkDir
	resource := filepath.Join(skillRoot, "references", "guide.md")
	if err := os.MkdirAll(filepath.Dir(resource), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resource, []byte("loaded from user skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	view := tool.NewViewTool()
	result, err := view.Invoke(context.Background(), &tool.UseContext{
		WorkDir:    workDir,
		SkillRoots: []string{skillRoot},
	}, json.RawMessage(`{"file_path":"references/guide.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Text, "loaded from user skill") {
		t.Fatalf("View result = %+v", result)
	}
}

func TestSkillRootPathsCannotEscapeRoot(t *testing.T) {
	workDir := t.TempDir()
	skillRoot := t.TempDir()
	outside := t.TempDir()
	uctx := &tool.UseContext{WorkDir: workDir, SkillRoots: []string{skillRoot}}
	if err := tool.CheckAllowedPath(uctx, filepath.Join(outside, "secret.md")); err == nil {
		t.Fatal("expected external path to be rejected")
	}
}
