package builtin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solosw/solcode/internal/skill/builtin"
)

// The bundled workflow skills must all load with a usable name, description,
// and non-empty instructions — a skill that loads but advertises nothing is
// worse than one that is absent.
func TestMaterializeBuiltinSkills(t *testing.T) {
	dir := t.TempDir()
	defs, err := builtin.MaterializeBuiltinSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) == 0 {
		t.Fatal("expected bundled skills")
	}
	seen := map[string]bool{}
	for _, def := range defs {
		if def.Name == "" {
			t.Fatalf("skill with empty name: %+v", def)
		}
		if seen[def.Name] {
			t.Fatalf("duplicate skill %q", def.Name)
		}
		seen[def.Name] = true
		if strings.TrimSpace(def.Description) == "" {
			t.Fatalf("skill %q has no description", def.Name)
		}
		body, err := def.ReadInstructions()
		if err != nil {
			t.Fatalf("skill %q: %v", def.Name, err)
		}
		if strings.TrimSpace(body) == "" {
			t.Fatalf("skill %q has empty instructions", def.Name)
		}
		if !def.IsPackage() {
			t.Fatalf("skill %q should be a package with a root", def.Name)
		}
		if _, err := os.Stat(filepath.Join(def.Root(), "SKILL.md")); err != nil {
			t.Fatalf("skill %q root is not materialized: %v", def.Name, err)
		}
	}
}

// The advertised names must match the frontmatter name, because Jev routes by
// name and the Skill tool looks the definition up by it.
func TestBuiltinSkillsAreNamedAfterTheirDirectories(t *testing.T) {
	dir := t.TempDir()
	defs, err := builtin.MaterializeBuiltinSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	names, err := builtin.BuiltinSkillNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != len(names) {
		t.Fatalf("materialized %d skills, embedded %d", len(defs), len(names))
	}
	got := make([]string, 0, len(defs))
	for _, def := range defs {
		got = append(got, def.Name)
	}
	for i, name := range names {
		if got[i] != name {
			t.Fatalf("skills = %v, embedded = %v", got, names)
		}
	}
}

// The bundle must cover the core agent workflows, since these are what Jev
// routes between. A missing one silently changes routing behavior.
func TestBuiltinSkillsCoverCoreWorkflows(t *testing.T) {
	names, err := builtin.BuiltinSkillNames()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, name := range names {
		have[name] = true
	}
	for _, want := range []string{"explore", "implement", "verify", "research", "debug", "review"} {
		if !have[want] {
			t.Fatalf("missing bundled skill %q (have %v)", want, names)
		}
	}
}

// Re-materializing over an existing cache directory must be idempotent, since
// this runs on every startup.
func TestMaterializeBuiltinSkillsIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := builtin.MaterializeBuiltinSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builtin.MaterializeBuiltinSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("first %d, second %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("order or names changed: %v vs %v", first[i].Name, second[i].Name)
		}
	}
}

// Only bundled skills are returned: a foreign skill sitting in the same cache
// directory belongs to normal skill-directory loading, not to this bundle.
func TestMaterializeBuiltinSkillsIgnoresForeignSkills(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "user-authored")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: user-authored\ndescription: mine\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := builtin.MaterializeBuiltinSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.Name == "user-authored" {
			t.Fatal("a foreign skill must not be adopted into the bundle")
		}
	}
}

func TestMaterializeBuiltinSkillsRequiresCacheDir(t *testing.T) {
	if _, err := builtin.MaterializeBuiltinSkills("  "); err == nil {
		t.Fatal("expected an error for an empty cache dir")
	}
}
