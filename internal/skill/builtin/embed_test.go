package builtin_test

import (
	"path/filepath"
	"testing"

	"github.com/solosw/solcode/internal/skill/builtin"
)

func TestMaterializeComputerUseSkill(t *testing.T) {
	dir := t.TempDir()
	def, err := builtin.MaterializeComputerUseSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != builtin.ComputerUseSkillName {
		t.Fatalf("name = %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("missing description")
	}
	body, err := def.ReadInstructions()
	if err != nil {
		t.Fatal(err)
	}
	if body == "" {
		t.Fatal("empty instructions")
	}
	if filepath.Base(def.Path) != "SKILL.md" {
		t.Fatalf("path = %q", def.Path)
	}
}
