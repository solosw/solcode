package tool

import (
	"slices"
	"sort"
	"sync"
)

// Registry holds all registered tools and provides filtered lookups.
type Registry struct {
	mu    sync.RWMutex
	tools []Tool
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds one or more tools.
func (r *Registry) Register(tools ...Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append(r.tools, tools...)
}

// ReplaceAll replaces the registry contents with the given tools.
func (r *Registry) ReplaceAll(tools []Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append([]Tool(nil), tools...)
}

// All returns every registered tool sorted by name (stable ordering
// important for prompt cache stability).
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := make([]Tool, len(r.tools))
	copy(sorted, r.tools)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Name() < sorted[j].Name()
	})
	return sorted
}

// Filter returns tools whose names match the given whitelist.
// An empty whitelist means "all tools". nil means "no tools".
func (r *Registry) Filter(whitelist []string) []Tool {
	if whitelist == nil {
		return nil
	}
	if len(whitelist) == 0 {
		return r.All()
	}
	all := r.All()
	filtered := make([]Tool, 0, len(whitelist))
	for _, t := range all {
		if slices.Contains(whitelist, t.Name()) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// Find looks up a tool by name or alias. Returns nil if not found.
func (r *Registry) Find(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.tools {
		if t.Name() == name {
			return t
		}
		if slices.Contains(t.Aliases(), name) {
			return t
		}
	}
	return nil
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
