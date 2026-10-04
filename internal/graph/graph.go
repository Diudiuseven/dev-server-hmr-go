// Package graph holds the module dependency graph and computes the
// set of modules affected by a change in topological build order.
package graph

import "fmt"

// Graph is a directed dependency graph: an edge A -> B means A depends
// on B, therefore B must be built before A.
type Graph struct {
	order []string            // declaration order, used for stable output
	deps  map[string][]string // node -> its dependencies (predecessors)
	rdeps map[string][]string // node -> modules that transitively/immediately depend on it
}

// ModuleDecl is the minimal module description needed to build the graph.
type ModuleDecl struct {
	Name string
	Deps []string
}

// New builds a Graph from module dependency declarations.
// Unknown or cyclic dependencies result in an error.
func New(modules []ModuleDecl) (*Graph, error) {
	g := &Graph{
		deps:  make(map[string][]string),
		rdeps: make(map[string][]string),
	}
	index := make(map[string]int, len(modules))
	for i, m := range modules {
		if m.Name == "" {
			return nil, fmt.Errorf("graph: module with empty name")
		}
		if _, dup := index[m.Name]; dup {
			return nil, fmt.Errorf("graph: duplicate module %q", m.Name)
		}
		index[m.Name] = i
		g.order = append(g.order, m.Name)
		g.deps[m.Name] = nil
		g.rdeps[m.Name] = nil
	}
	for _, m := range modules {
		seen := make(map[string]bool, len(m.Deps))
		for _, d := range m.Deps {
			if _, ok := index[d]; !ok {
				return nil, fmt.Errorf("graph: module %q depends on unknown module %q", m.Name, d)
			}
			if d == m.Name {
				return nil, fmt.Errorf("graph: module %q depends on itself", m.Name)
			}
			if seen[d] {
				continue
			}
			seen[d] = true
			g.deps[m.Name] = append(g.deps[m.Name], d)
			g.rdeps[d] = append(g.rdeps[d], m.Name)
		}
	}
	if _, err := g.topo(); err != nil {
		return nil, err
	}
	return g, nil
}

// Nodes returns all module names in declaration order.
func (g *Graph) Nodes() []string {
	out := make([]string, len(g.order))
	copy(out, g.order)
	return out
}

// Topological returns all modules in dependency-respecting build order.
// Ties are broken by declaration order for deterministic output.
func (g *Graph) Topological() []string {
	order, err := g.topo()
	if err != nil {
		return nil // validated at construction time
	}
	return order
}

func (g *Graph) topo() ([]string, error) {
	indeg := make(map[string]int, len(g.order)) // number of unbuilt dependencies
	for _, n := range g.order {
		indeg[n] = len(g.deps[n])
	}
	// Kahn's algorithm; declaration order substitutes for a priority queue.
	result := make([]string, 0, len(g.order))
	for len(result) < len(g.order) {
		picked := false
		for _, n := range g.order {
			if indeg[n] < 0 {
				continue // already emitted (use sentinel)
			}
			if indeg[n] == 0 {
				result = append(result, n)
				indeg[n] = -1
				picked = true
				for _, r := range g.rdeps[n] {
					indeg[r]--
				}
			}
		}
		if !picked {
			cyclic := []string{}
			for _, n := range g.order {
				if indeg[n] > 0 {
					cyclic = append(cyclic, n)
				}
			}
			return nil, fmt.Errorf("graph: dependency cycle involving %v", cyclic)
		}
	}
	return result, nil
}

// Affected returns the changed modules together with every module that
// transitively depends on them, in a valid topological build order.
// Unrelated modules are never included. Unknown input modules are ignored.
func (g *Graph) Affected(changed []string) []string {
	reachable := make(map[string]bool)
	stack := make([]string, 0, len(changed))
	for _, c := range changed {
		if _, ok := g.deps[c]; !ok {
			continue
		}
		if !reachable[c] {
			reachable[c] = true
			stack = append(stack, c)
		}
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, r := range g.rdeps[n] {
			if !reachable[r] {
				reachable[r] = true
				stack = append(stack, r)
			}
		}
	}
	topo := g.Topological()
	out := make([]string, 0, len(reachable))
	for _, n := range topo {
		if reachable[n] {
			out = append(out, n)
		}
	}
	return out
}
