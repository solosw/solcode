package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const skillFileName = "SKILL.md"

// Definition is a discovered skill package or standalone markdown skill.
type Definition struct {
	Name          string
	Description   string
	Path          string // path to SKILL.md or standalone .md
	Source        string // skill root directory (package) or parent dir (standalone)
	License       string
	Compatibility string
	AllowedTools  string
	Metadata      map[string]string
}

// BundledFiles lists optional package resources under the skill root.
type BundledFiles struct {
	Scripts    []string // paths relative to skill root, e.g. scripts/extract.py
	References []string
	Assets     []string
}

// Meta is the YAML frontmatter from a SKILL.md (Agent Skills specification).
type Meta struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	AllowedTools  string            `yaml:"allowed-tools"`
	Metadata      map[string]string `yaml:"metadata"`
}

type Registry struct {
	defs map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{defs: make(map[string]Definition)}
}

func (r *Registry) Add(def Definition) {
	name := normalizeName(def.Name)
	if name == "" {
		return
	}
	def.Name = name
	r.defs[name] = def
}

func (r *Registry) All() []Definition {
	if r == nil {
		return nil
	}
	out := make([]Definition, 0, len(r.defs))
	for _, def := range r.defs {
		out = append(out, def)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) Find(name string) (Definition, bool) {
	if r == nil {
		return Definition{}, false
	}
	def, ok := r.defs[normalizeName(name)]
	return def, ok
}

func LoadFromDirs(dirs ...string) *Registry {
	registry := NewRegistry()
	for _, dir := range dirs {
		loadFromDir(registry, dir)
	}
	return registry
}

func loadFromDir(registry *Registry, dir string) {
	if registry == nil || dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if skillPath, ok := skillFileFromEntries(dir, entries); ok {
		registry.Add(definitionFromFile(filepath.Base(filepath.Clean(dir)), skillPath, dir))
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			subdir := filepath.Join(dir, entry.Name())
			if skillPath, ok := skillFileInDir(subdir); ok {
				registry.Add(definitionFromFile(entry.Name(), skillPath, subdir))
			}
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		// Standalone markdown skills (not SKILL.md package layout).
		if strings.EqualFold(entry.Name(), skillFileName) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		registry.Add(definitionFromFile(name, path, dir))
	}
}

func definitionFromFile(nameHint, path, source string) Definition {
	def := Definition{
		Name:        nameHint,
		Description: fmt.Sprintf("Skill %s", normalizeName(nameHint)),
		Path:        path,
		Source:      source,
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	meta, _, hasMeta := ParseDocument(string(raw))
	if hasMeta {
		// Registry key stays the directory / file-stem hint so slash commands
		// (/review) keep working. Spec wants frontmatter name to match; if it
		// does, accept it (same value). If it differs, keep the path-derived name.
		if n := normalizeName(meta.Name); n != "" && n == normalizeName(nameHint) {
			def.Name = n
		}
		if desc := strings.TrimSpace(meta.Description); desc != "" {
			def.Description = desc
		}
		def.License = strings.TrimSpace(meta.License)
		def.Compatibility = strings.TrimSpace(meta.Compatibility)
		def.AllowedTools = strings.TrimSpace(meta.AllowedTools)
		if len(meta.Metadata) > 0 {
			def.Metadata = meta.Metadata
		}
	}
	return def
}

// ParseDocument splits optional YAML frontmatter from the markdown body.
// hasMeta is true only when a well-formed --- ... --- block is present.
func ParseDocument(content string) (meta Meta, body string, hasMeta bool) {
	front, rest, ok := splitFrontmatter(content)
	if !ok {
		return Meta{}, strings.TrimSpace(content), false
	}
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		// Malformed frontmatter: treat whole file as body so the skill still loads.
		return Meta{}, strings.TrimSpace(content), false
	}
	return meta, strings.TrimSpace(rest), true
}

func splitFrontmatter(content string) (front string, body string, ok bool) {
	content = strings.TrimPrefix(content, "\uFEFF")
	// Normalize newlines for scanning only; body is rebuilt from original lines.
	text := strings.ReplaceAll(content, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if !strings.HasPrefix(text, "---\n") && text != "---" {
		return "", content, false
	}
	rest := strings.TrimPrefix(text, "---\n")
	if text == "---" {
		return "", content, false
	}
	// Closing fence must be a line that is exactly ---
	idx := strings.Index(rest, "\n---\n")
	endOnly := false
	if idx < 0 {
		if strings.HasSuffix(rest, "\n---") {
			idx = len(rest) - len("\n---")
			endOnly = true
		} else if rest == "---" {
			return "", "", true
		} else {
			return "", content, false
		}
	}
	front = rest[:idx]
	if endOnly {
		body = ""
	} else {
		body = rest[idx+len("\n---\n"):]
	}
	return front, body, true
}

// Root returns the skill package directory.
func (d Definition) Root() string {
	if strings.TrimSpace(d.Source) != "" {
		return d.Source
	}
	if d.Path == "" {
		return ""
	}
	return filepath.Dir(d.Path)
}

// IsPackage reports whether this skill uses the SKILL.md directory layout
// (eligible for scripts/references/assets).
func (d Definition) IsPackage() bool {
	return strings.EqualFold(filepath.Base(d.Path), skillFileName)
}

// ReadInstructions returns the skill markdown body without frontmatter.
func (d Definition) ReadInstructions() (string, error) {
	raw, err := os.ReadFile(d.Path)
	if err != nil {
		return "", err
	}
	_, body, _ := ParseDocument(string(raw))
	return body, nil
}

// ListBundledFiles discovers optional resource files under the skill root.
// Only package-layout skills (SKILL.md) participate; standalone .md skills return empty.
func (d Definition) ListBundledFiles() BundledFiles {
	if !d.IsPackage() {
		return BundledFiles{}
	}
	root := d.Root()
	if root == "" {
		return BundledFiles{}
	}
	return BundledFiles{
		Scripts:    listRelFiles(root, "scripts"),
		References: listRelFiles(root, "references"),
		Assets:     listRelFiles(root, "assets"),
	}
}

func listRelFiles(root, subdir string) []string {
	dir := filepath.Join(root, subdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			// One level deep only (Agent Skills guidance).
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, filepath.ToSlash(filepath.Join(subdir, name)))
	}
	sort.Strings(out)
	return out
}

func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	return strings.ToLower(name)
}

func skillFileInDir(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	return skillFileFromEntries(dir, entries)
}

func skillFileFromEntries(dir string, entries []os.DirEntry) (string, bool) {
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.EqualFold(entry.Name(), skillFileName) {
			return filepath.Join(dir, entry.Name()), true
		}
	}
	return "", false
}
