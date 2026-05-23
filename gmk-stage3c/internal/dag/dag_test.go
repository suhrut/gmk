package dag

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestTopoSort_Empty(t *testing.T) {
	got, err := TopoSort(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestTopoSort_SingleNode(t *testing.T) {
	got, err := TopoSort(map[string][]string{"a": nil}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTopoSort_LinearChain(t *testing.T) {
	// a -> b -> c. Order should be c, b, a (deps before dependents).
	nodes := map[string][]string{
		"a": {"b"},
		"b": {"c"},
		"c": nil,
	}
	got, err := TopoSort(nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"c", "b", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTopoSort_Diamond(t *testing.T) {
	// a depends on b and c; b and c both depend on d.
	//   a -> {b, c} -> d
	// Order: d, b, c, a  (or d, c, b, a — alphabetical tie-break gives b before c)
	nodes := map[string][]string{
		"a": {"b", "c"},
		"b": {"d"},
		"c": {"d"},
		"d": nil,
	}
	got, err := TopoSort(nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	// d must come first; a must come last; b and c between.
	if got[0] != "d" {
		t.Errorf("first should be d, got %v", got)
	}
	if got[len(got)-1] != "a" {
		t.Errorf("last should be a, got %v", got)
	}
	if len(got) != 4 {
		t.Errorf("expected 4 nodes, got %d: %v", len(got), got)
	}
	// Deterministic: same input -> same output across runs.
	got2, _ := TopoSort(nodes, nil)
	if !reflect.DeepEqual(got, got2) {
		t.Errorf("non-deterministic: first %v second %v", got, got2)
	}
}

func TestTopoSort_PartialScheduling(t *testing.T) {
	// 5 nodes, but only request 'a' which depends on b, c.
	// d and e should not be scheduled.
	nodes := map[string][]string{
		"a": {"b", "c"},
		"b": nil,
		"c": nil,
		"d": nil,
		"e": nil,
	}
	got, err := TopoSort(nodes, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 (a,b,c), got %v", got)
	}
	for _, name := range got {
		if name == "d" || name == "e" {
			t.Errorf("unscheduled node %q appeared in output", name)
		}
	}
}

func TestTopoSort_MultipleRoots(t *testing.T) {
	nodes := map[string][]string{
		"a":         {"shared"},
		"b":         {"shared"},
		"shared":    nil,
		"unrelated": nil,
	}
	got, err := TopoSort(nodes, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 (a,b,shared), got %v", got)
	}
	// shared must precede both a and b.
	pos := func(s string) int {
		for i, n := range got {
			if n == s {
				return i
			}
		}
		return -1
	}
	if pos("shared") > pos("a") || pos("shared") > pos("b") {
		t.Errorf("shared must come before a and b, got %v", got)
	}
}

func TestTopoSort_DirectCycle(t *testing.T) {
	nodes := map[string][]string{
		"a": {"b"},
		"b": {"a"},
	}
	_, err := TopoSort(nodes, nil)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !errors.Is(err, ErrCycle) {
		t.Errorf("err should wrap ErrCycle, got %v", err)
	}
	if !strings.Contains(err.Error(), "->") {
		t.Errorf("err should show cycle path, got %v", err)
	}
}

func TestTopoSort_IndirectCycle(t *testing.T) {
	// a -> b -> c -> a
	nodes := map[string][]string{
		"a": {"b"},
		"b": {"c"},
		"c": {"a"},
	}
	_, err := TopoSort(nodes, nil)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !errors.Is(err, ErrCycle) {
		t.Errorf("err should wrap ErrCycle, got %v", err)
	}
}

func TestTopoSort_SelfLoop(t *testing.T) {
	nodes := map[string][]string{"a": {"a"}}
	_, err := TopoSort(nodes, nil)
	if err == nil {
		t.Fatal("expected cycle error for self-loop")
	}
	if !errors.Is(err, ErrCycle) {
		t.Errorf("self-loop should be ErrCycle, got %v", err)
	}
}

func TestTopoSort_MissingDep(t *testing.T) {
	nodes := map[string][]string{
		"a": {"ghost"},
	}
	_, err := TopoSort(nodes, nil)
	if err == nil {
		t.Fatal("expected missing-node error")
	}
	if !errors.Is(err, ErrMissingNode) {
		t.Errorf("err should wrap ErrMissingNode, got %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err should mention missing dep name, got %v", err)
	}
	if !strings.Contains(err.Error(), "a") {
		t.Errorf("err should mention referrer, got %v", err)
	}
}

func TestTopoSort_MissingRoot(t *testing.T) {
	nodes := map[string][]string{"exists": nil}
	_, err := TopoSort(nodes, []string{"ghost"})
	if err == nil {
		t.Fatal("expected missing-node error for non-existent root")
	}
	if !errors.Is(err, ErrMissingNode) {
		t.Errorf("err should wrap ErrMissingNode, got %v", err)
	}
}

func TestTopoSort_DeterministicTies(t *testing.T) {
	// 4 independent nodes — order should be alphabetical.
	nodes := map[string][]string{
		"d": nil,
		"a": nil,
		"c": nil,
		"b": nil,
	}
	got, err := TopoSort(nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (alphabetical tie-break)", got, want)
	}
}

func TestTopoSort_LargerGraph(t *testing.T) {
	// Realistic build graph.
	nodes := map[string][]string{
		"all":      {"binary", "tests"},
		"binary":   {"compile"},
		"tests":    {"compile"},
		"compile":  {"generate"},
		"generate": nil,
	}
	got, err := TopoSort(nodes, []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	pos := func(s string) int {
		for i, n := range got {
			if n == s {
				return i
			}
		}
		return -1
	}
	if pos("generate") > pos("compile") {
		t.Errorf("generate must come before compile, got %v", got)
	}
	if pos("compile") > pos("binary") {
		t.Errorf("compile must come before binary, got %v", got)
	}
	if pos("binary") > pos("all") {
		t.Errorf("binary must come before all, got %v", got)
	}
}
