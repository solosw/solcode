package unit_tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/config"
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
	}, json.RawMessage(`{"path":"references/guide.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Text, "loaded from user skill") {
		t.Fatalf("View result = %+v", result)
	}
}

func TestCheckAllowedPathAllowsAnyDirectory(t *testing.T) {
	workDir := t.TempDir()
	skillRoot := t.TempDir()
	outside := t.TempDir()
	uctx := &tool.UseContext{WorkDir: workDir, SkillRoots: []string{skillRoot}}
	if err := tool.CheckAllowedPath(uctx, filepath.Join(outside, "secret.md")); err != nil {
		t.Fatalf("CheckAllowedPath() external path rejected: %v", err)
	}
}

func TestCheckAllowedPathAllowsUserSolcodeDir(t *testing.T) {
	workDir := t.TempDir()
	userRoot := config.UserConfigDir()
	if userRoot == "" {
		t.Fatal("expected UserConfigDir to be non-empty")
	}
	target := filepath.Join(userRoot, "settings.local.json")
	uctx := &tool.UseContext{WorkDir: workDir}
	if err := tool.CheckAllowedPath(uctx, target); err != nil {
		t.Fatalf("CheckAllowedPath() user .solcode path rejected: %v", err)
	}
}

func TestCheckAllowedPathAllowsOutsideUserSolcode(t *testing.T) {
	workDir := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "not-solcode-secret.txt")
	uctx := &tool.UseContext{WorkDir: workDir}
	if err := tool.CheckAllowedPath(uctx, outside); err != nil {
		t.Fatalf("CheckAllowedPath() path outside ~/.solcode rejected: %v", err)
	}
}
