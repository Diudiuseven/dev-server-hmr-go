package graph

import (
	"reflect"
	"testing"
)

func TestTopoOrderRespectsDependencies(t *testing.T) {
	// a <- b <- c (b depends on a, c depends on b); d is independent.
	g, err := New(map[string][]string{
		"a": nil,
		"b": {"a"},
		"c": {"b"},
		"d": nil,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	order, err := g.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	if pos["a"] > pos["b"] || pos["b"] > pos["c"] {
		t.Fatalf("dependency order violated: %v", order)
	}
	if !reflect.DeepEqual(order, []string{"a", "b", "c", "d"}) {
		t.Fatalf("unexpected deterministic order: %v", order)
	}
}

func TestCycleRejected(t *testing.T) {
	if _, err := New(map[string][]string{
		"a": {"c"},
		"b": {"a"},
		"c": {"b"},
	}); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestUnknownDependencyRejected(t *testing.T) {
	if _, err := New(map[string][]string{"a": {"ghost"}}); err == nil {
		t.Fatal("expected unknown dependency error, got nil")
	}
}

func TestAffectedPropagatesToDependentsOnly(t *testing.T) {
	// a -> b -> c ; b -> d ; e unrelated.
	g, err := New(map[string][]string{
		"a": nil,
		"b": {"a"},
		"c": {"b"},
		"d": {"b"},
		"e": nil,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := g.Affected([]string{"a"})
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Affected(a) = %v, want %v (unrelated e must be excluded)", got, want)
	}
}

func TestAffectedMidstream(t *testing.T) {
	g, err := New(map[string][]string{
		"a": nil,
		"b": {"a"},
		"c": {"b"},
		"d": {"b"},
		"e": nil,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Changing b rebuilds b,c,d but not a.
	got := g.Affected([]string{"b"})
	want := []string{"b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Affected(b) = %v, want %v", got, want)
	}
}

func TestAffectedDedupes(t *testing.T) {
	g, _ := New(map[string][]string{
		"a": nil,
		"b": {"a"},
		"c": {"a", "b"},
	})
	got := g.Affected([]string{"a", "a", "b"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Affected = %v, want %v", got, want)
	}
}
