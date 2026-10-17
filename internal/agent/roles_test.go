package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuiltinRoles(t *testing.T) {
	entries, err := builtinRoles.ReadDir("roles")
	if err != nil || len(entries) != 3 {
		t.Fatalf("builtins: %v %v", entries, err)
	}
	for _, name := range []string{"explorer", "worker", "reviewer"} {
		role, err := loadRole(name, nil)
		if err != nil || role.Description == "" || role.Instructions == "" || !IsBuiltinType(name) {
			t.Fatalf("role %s: %+v %v", name, role, err)
		}
		if name != "worker" {
			if len(role.AllowedTools) == 0 {
				t.Fatalf("%s is unrestricted", name)
			}
			for _, tool := range role.AllowedTools {
				if tool == "Bash" || tool == "Write" || tool == "Task" {
					t.Fatalf("%s permits %s", name, tool)
				}
			}
		}
	}
}

func TestRoleOverridesAndValidation(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(user, "explorer", "model = 'fast'\nallowed_tools = ['View']\n")
	write(project, "explorer", "instructions = 'Project-specific exploration'\n")
	role, err := loadRole("explorer", []string{user, project})
	if err != nil || role.Model != "fast" || role.Instructions != "Project-specific exploration" || !reflect.DeepEqual(role.AllowedTools, []string{"View"}) || role.Description == "" {
		t.Fatalf("layering: %+v %v", role, err)
	}
	write(project, "explorer", "model = 'project-model'\nallowed_tools = []\n")
	role, err = loadRole("explorer", []string{user, project})
	if err != nil || role.Model != "project-model" || len(role.AllowedTools) != 0 || role.Instructions == "" {
		t.Fatalf("replacement: %+v %v", role, err)
	}
	write(project, "specialist", "instructions = 'Custom work'\nmodel = 'custom-model'\n")
	if role, err := loadRole("specialist", []string{project}); err != nil || role.Model != "custom-model" || IsBuiltinType("specialist") {
		t.Fatalf("custom: %+v %v", role, err)
	}
	for _, name := range []string{"missing", "../worker", "/worker", "Worker", ""} {
		if _, err := loadRole(name, []string{project}); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	write(project, "worker", "permission_mode = 'bypass'\n")
	if _, err := loadRole("worker", []string{project}); err == nil || !strings.Contains(err.Error(), "worker.toml") {
		t.Fatalf("unknown authority setting: %v", err)
	}
	write(project, "worker", "model = [\n")
	if _, err := loadRole("worker", []string{project}); err == nil {
		t.Fatal("accepted malformed TOML")
	}
}
