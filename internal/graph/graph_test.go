package graph

import (
	"reflect"
	"testing"
)

func decls() []ModuleDecl {
	// a -> b -> c ; d independent
	return []ModuleDecl{
		{Name: "a", Deps: []string{"b"}},
		{Name: "b", Deps: []string{"c"}},
		{Name: "c"},
		{Name: "d"},
	}
}

func TestTopological(t *testing.T) {
	g, err := New(decls())
	if err != nil {
		t.Fatal(err)
	}
	got := g.Topological()
	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	if pos["c"] > pos["b"] || pos["b"] > pos["a"] {
		t.Fatalf("dependency order violated: %v", got)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 nodes, got %v", got)
	}
}

func TestAffectedPropagatesToDependents(t *testing.T) {
	g, err := New(decls())
	if err != nil {
		t.Fatal(err)
	}
	got := g.Affected([]string{"c"})
	want := []string{"c", "b", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Affected(c) = %v, want %v", got, want)
	}
	got = g.Affected([]string{"b"})
	want = []string{"b", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Affected(b) = %v, want %v", got, want)
	}
}

func TestAffectedExcludesUnrelated(t *testing.T) {
	g, _ := New(decls())
	for _, c := range g.Affected([]string{"c"}) {
		if c == "d" {
			t.Fatal("unrelated module d must not be rebuilt")
		}
	}
}

func TestAffectedDedup(t *testing.T) {
	g, _ := New([]ModuleDecl{
		{Name: "a", Deps: []string{"b", "c"}},
		{Name: "b", Deps: []string{"d"}},
		{Name: "c", Deps: []string{"d"}},
		{Name: "d"},
	})
	got := g.Affected([]string{"d", "d"})
	want := []string{"d", "b", "c", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Affected = %v, want %v", got, want)
	}
}

func TestCycleAndUnknown(t *testing.T) {
	if _, err := New([]ModuleDecl{{Name: "a", Deps: []string{"b"}}, {Name: "b", Deps: []string{"a"}}}); err == nil {
		t.Fatal("expected cycle error")
	}
	if _, err := New([]ModuleDecl{{Name: "a", Deps: []string{"x"}}}); err == nil {
		t.Fatal("expected unknown dep error")
	}
}
