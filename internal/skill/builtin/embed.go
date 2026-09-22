package builtin

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/solosw/solcode/internal/skill"
)

// computerUseFS holds the bundled computer-use skill, which is gated on its own
// setting rather than on Jev.
//
//go:embed computer-use/SKILL.md
var computerUseFS embed.FS

// builtinFS holds the workflow skills bundled with solcode. They are registered
// only when the Jev decision layer is doing skill selection, because otherwise
// they would be one more catalog the chat model has to reason about on its own.
//
//go:embed skills/*/SKILL.md
var builtinFS embed.FS

// ComputerUseSkillName is the bundled desktop-automation skill.
const ComputerUseSkillName = "computer-use"

// builtinSkillsDir is the directory under the embed FS holding workflow skills.
const builtinSkillsDir = "skills"

// MaterializeComputerUseSkill writes the bundled computer-use skill to a cache
// directory and returns a skill Definition ready for registry.Add.
func MaterializeComputerUseSkill(cacheDir string) (skill.Definition, error) {
	if strings.TrimSpace(cacheDir) == "" {
		return skill.Definition{}, fmt.Errorf("cacheDir is required")
	}
	root := filepath.Join(cacheDir, ComputerUseSkillName)
	if err := writeSkillFile(root, computerUseFS, "computer-use/SKILL.md"); err != nil {
		return skill.Definition{}, err
	}
	loaded := skill.LoadFromDirs(cacheDir)
	def, ok := loaded.Find(ComputerUseSkillName)
	if !ok {
		return skill.Definition{}, fmt.Errorf("bundled skill %q failed to load", ComputerUseSkillName)
	}
	return def, nil
}

// MaterializeBuiltinSkills writes every bundled workflow skill to cacheDir and
// returns them as Definitions, sorted by name.
//
// The files are written to disk rather than served from the embed FS because a
// skill's advertised value is its Root: the Skill tool resolves scripts/,
// references/, and assets/ against a real directory, and the model may need to
// open them with View or Bash.
func MaterializeBuiltinSkills(cacheDir string) ([]skill.Definition, error) {
	if strings.TrimSpace(cacheDir) == "" {
		return nil, fmt.Errorf("cacheDir is required")
	}
	entries, err := fs.ReadDir(builtinFS, builtinSkillsDir)
	if err != nil {
		return nil, fmt.Errorf("read bundled skills: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	for _, name := range names {
		src := builtinSkillsDir + "/" + name + "/" + skill.SkillFileName
		if err := writeSkillFile(filepath.Join(cacheDir, name), builtinFS, src); err != nil {
			return nil, err
		}
	}
	loaded := skill.LoadFromDirs(cacheDir)
	out := make([]skill.Definition, 0, len(names))
	for _, def := range loaded.All() {
		if _, err := fs.Stat(builtinFS, builtinSkillsDir+"/"+def.Name); err != nil {
			// A skill in the cache directory that we did not put there. Leave it
			// to the normal skill directory loading instead of adopting it here.
			continue
		}
		out = append(out, def)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// BuiltinSkillNames lists the bundled workflow skill names without touching the
// filesystem. Useful for tests and for explaining what the bundle contains.
func BuiltinSkillNames() ([]string, error) {
	entries, err := fs.ReadDir(builtinFS, builtinSkillsDir)
	if err != nil {
		return nil, fmt.Errorf("read bundled skills: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// writeSkillFile materializes one embedded SKILL.md at root/SKILL.md.
func writeSkillFile(root string, source fs.FS, srcPath string) error {
	data, err := fs.ReadFile(source, srcPath)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", srcPath, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", root, err)
	}
	path := filepath.Join(root, skill.SkillFileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
