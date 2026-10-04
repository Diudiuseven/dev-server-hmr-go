package graph

import (
	"fmt"
	"sort"
)

// Graph is the static dependency graph of the workspace.
// Edge convention: dependencies point to dependents (dep -> modules that
// depend on it), so change propagation follows the edges forward.
type Graph struct {
	names []string
	deps  map[string][]string // module -> modules it depends on
	rdeps map[string][]string // module -> modules depending on it
	order []string            // dependency-first topological order
}

// New builds a Graph from name -> dependency list pairs.
// It rejects cycles and references to unknown modules. The topological
// order is made deterministic by sorting modules with equal priority.
func New(modules map[string][]string) (*Graph, error) {
	g := &Graph{
		deps:  make(map[string][]string, len(modules)),
		rdeps: make(map[string][]string, len(modules)),
	}
	for name := range modules {
		g.names = append(g.names, name)
	}
	sort.Strings(g.names)
	for _, name := range g.names {
		// Ensure every module has an entry even when it has no deps,
		// otherwise membership checks treat leaf modules as unknown.
		g.deps[name] = nil
		deps := append([]string(nil), modules[name]...)
		sort.Strings(deps)
		seen := map[string]bool{}
		for _, dep := range deps {
			if dep == name {
				return nil, fmt.Errorf("module %q depends on itself", name)
			}
			if _, ok := modules[dep]; !ok {
				return nil, fmt.Errorf("module %q depends on unknown module %q", name, dep)
			}
			if seen[dep] {
				continue
			}
			seen[dep] = true
			g.deps[name] = append(g.deps[name], dep)
			g.rdeps[dep] = append(g.rdeps[dep], name)
		}
	}
	order, err := g.topoSort()
	if err != nil {
		return nil, err
	}
	g.order = order
	return g, nil
}

// topoSort performs Kahn's algorithm; ties are broken by name.
func (g *Graph) topoSort() ([]string, error) {
	indeg := make(map[string]int, len(g.names))
	for _, name := range g.names {
		indeg[name] = len(g.deps[name])
	}
	// ready is kept sorted by name for deterministic output.
	var ready []string
	for _, name := range g.names {
		if indeg[name] == 0 {
			ready = append(ready, name)
		}
	}
	order := make([]string, 0, len(g.names))
	for len(ready) > 0 {
		cur := ready[0]
		ready = ready[1:]
		order = append(order, cur)
		// rdeps[cur] is already sorted because names were processed
		// in sorted order and appended in sorted-deps order.
		for _, next := range g.rdeps[cur] {
			indeg[next]--
			if indeg[next] == 0 {
				ready = insertSorted(ready, next)
			}
		}
	}
	if len(order) != len(g.names) {
		var cyclic []string
		for _, name := range g.names {
			if indeg[name] > 0 {
				cyclic = append(cyclic, name)
			}
		}
		sort.Strings(cyclic)
		return nil, fmt.Errorf("dependency cycle detected involving: %v", cyclic)
	}
	return order, nil
}

func insertSorted(s []string, v string) []string {
	i := sort.SearchStrings(s, v)
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// Names returns all module names in a stable sorted order.
func (g *Graph) Names() []string {
	out := append([]string(nil), g.names...)
	return out
}

// TopoOrder returns the module names in dependency-first order.
func (g *Graph) TopoOrder() ([]string, error) {
	out := append([]string(nil), g.order...)
	return out, nil
}

// DepsOf returns the direct dependencies of a module.
func (g *Graph) DepsOf(name string) []string {
	out := append([]string(nil), g.deps[name]...)
	return out
}

// Affected returns the changed modules plus every transitive dependent,
// in a dependency-first build order. Unrelated modules are excluded.
// Unknown names are ignored.
func (g *Graph) Affected(changed []string) []string {
	affected := make(map[string]bool, len(changed))
	var stack []string
	for _, name := range changed {
		if _, ok := g.deps[name]; !ok {
			continue
		}
		if !affected[name] {
			affected[name] = true
			stack = append(stack, name)
		}
	}
	// Forward traversal along reverse edges collects all dependents.
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, dep := range g.rdeps[cur] {
			if !affected[dep] {
				affected[dep] = true
				stack = append(stack, dep)
			}
		}
	}
	// Filter the global topological order so the returned slice is
	// guaranteed to respect dependency-first sequencing.
	out := make([]string, 0, len(affected))
	for _, name := range g.order {
		if affected[name] {
			out = append(out, name)
		}
	}
	return out
}
