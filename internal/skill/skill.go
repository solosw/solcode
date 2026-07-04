package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Definition struct {
	Name        string
	Description string
	Path        string
	Source      string
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
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		registry.Add(Definition{
			Name:        name,
			Description: fmt.Sprintf("Skill file %s", entry.Name()),
			Path:        filepath.Join(dir, entry.Name()),
			Source:      dir,
		})
	}
}

func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	return strings.ToLower(name)
}
