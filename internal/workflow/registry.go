package workflow

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Registry holds loaded workflow definitions keyed by normalized name.
type Registry struct {
	defs map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{defs: make(map[string]Definition)}
}

func (r *Registry) Add(def Definition) {
	if r == nil {
		return
	}
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

func (r *Registry) Names() []string {
	defs := r.All()
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}

// LoadFromDirs scans directories for workflow definition files.
// Layouts:
//   - name/workflow.yaml (or .yml / .json)
//   - name.yaml / name.yml / name.json
//
// Later directories win on name collision.
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
	// Directory itself is a single workflow package (workflow.yaml at root of dir).
	if path, ok := workflowFileFromEntries(dir, entries); ok {
		if def, err := ParseFile(path); err == nil {
			if def.Name == "" || def.Name == "workflow" {
				def.Name = normalizeName(filepath.Base(filepath.Clean(dir)))
			}
			def.Source = dir
			registry.Add(def)
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			subdir := filepath.Join(dir, entry.Name())
			if path, ok := workflowFileInDir(subdir); ok {
				if def, err := ParseFile(path); err == nil {
					if def.Name == "" || def.Name == "workflow" {
						def.Name = normalizeName(entry.Name())
					}
					def.Source = subdir
					registry.Add(def)
				}
			}
			continue
		}
		name := entry.Name()
		if !isWorkflowExt(name) {
			continue
		}
		// Skip generic workflow.* at top level of multi-workflow dir (handled above).
		if isWorkflowFileName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if def, err := ParseFile(path); err == nil {
			def.Source = dir
			registry.Add(def)
		}
	}
}

func workflowFileInDir(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	return workflowFileFromEntries(dir, entries)
}

func workflowFileFromEntries(dir string, entries []os.DirEntry) (string, bool) {
	// Prefer yaml then yml then json.
	preferred := []string{"workflow.yaml", "workflow.yml", "workflow.json"}
	lowerToPath := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lowerToPath[strings.ToLower(entry.Name())] = filepath.Join(dir, entry.Name())
	}
	for _, name := range preferred {
		if path, ok := lowerToPath[name]; ok {
			return path, true
		}
	}
	return "", false
}

func isWorkflowExt(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".json")
}
