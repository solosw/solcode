package builtin

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/solosw/solcode/internal/skill"
)

//go:embed computer-use/SKILL.md
var computerUseFS embed.FS

const ComputerUseSkillName = "computer-use"

// MaterializeComputerUseSkill writes the bundled computer-use skill to a cache
// directory and returns a skill Definition ready for registry.Add.
func MaterializeComputerUseSkill(cacheDir string) (skill.Definition, error) {
	if strings.TrimSpace(cacheDir) == "" {
		return skill.Definition{}, fmt.Errorf("cacheDir is required")
	}
	root := filepath.Join(cacheDir, "computer-use")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return skill.Definition{}, err
	}
	data, err := fs.ReadFile(computerUseFS, "computer-use/SKILL.md")
	if err != nil {
		return skill.Definition{}, err
	}
	path := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return skill.Definition{}, err
	}
	loaded := skill.LoadFromDirs(cacheDir)
	def, ok := loaded.Find(ComputerUseSkillName)
	if !ok {
		return skill.Definition{}, fmt.Errorf("bundled skill %q failed to load", ComputerUseSkillName)
	}
	return def, nil
}
