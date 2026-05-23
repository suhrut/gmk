package scope

import (
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/ir"
)

func makeScope(path string, parent *ir.Scope, vars map[string]string) *ir.Scope {
	s := &ir.Scope{
		Path:     path,
		Parent:   parent,
		Vars:     make(map[string]*ir.Var, len(vars)),
		VarOrder: make([]string, 0, len(vars)),
	}
	for k, v := range vars {
		s.Vars[k] = &ir.Var{
			Name:   k,
			Value:  v,
			Source: ir.SourceLoc{File: path, Line: 1},
		}
		s.VarOrder = append(s.VarOrder, k)
	}
	return s
}

func TestLookup_NilScope(t *testing.T) {
	v, s := Lookup(nil, "x")
	if v != nil || s != nil {
		t.Errorf("Lookup(nil, ...) = (%v, %v), want (nil, nil)", v, s)
	}
}

func TestLookup_EmptyName(t *testing.T) {
	scope := makeScope("/", nil, map[string]string{"x": "1"})
	v, s := Lookup(scope, "")
	if v != nil || s != nil {
		t.Errorf("Lookup with empty name returned non-nil")
	}
}

func TestLookup_DirectHit(t *testing.T) {
	scope := makeScope("/", nil, map[string]string{"x": "1", "y": "2"})

	v, found := Lookup(scope, "x")
	if v == nil {
		t.Fatal("expected to find x")
	}
	if v.Value != "1" {
		t.Errorf("v.Value = %q, want %q", v.Value, "1")
	}
	if found != scope {
		t.Errorf("found scope should be the queried scope")
	}
}

func TestLookup_NotFound(t *testing.T) {
	scope := makeScope("/", nil, map[string]string{"x": "1"})
	v, s := Lookup(scope, "missing")
	if v != nil || s != nil {
		t.Errorf("expected not found, got (%v, %v)", v, s)
	}
}

func TestLookup_ParentInheritance(t *testing.T) {
	parent := makeScope("/", nil, map[string]string{"shared": "from_parent"})
	child := makeScope("/target[x]", parent, map[string]string{"local": "in_child"})

	v, found := Lookup(child, "shared")
	if v == nil {
		t.Fatal("expected to find shared via parent")
	}
	if v.Value != "from_parent" {
		t.Errorf("v.Value = %q, want %q", v.Value, "from_parent")
	}
	if found != parent {
		t.Errorf("found should be parent scope")
	}
}

func TestLookup_ChildOverridesParent(t *testing.T) {
	parent := makeScope("/", nil, map[string]string{"x": "parent_val"})
	child := makeScope("/c", parent, map[string]string{"x": "child_val"})

	v, found := Lookup(child, "x")
	if v.Value != "child_val" {
		t.Errorf("expected child override; got %q", v.Value)
	}
	if found != child {
		t.Error("found should be child (override site)")
	}
}

func TestLookup_IncludeProvidesVar(t *testing.T) {
	// Parent scope is empty; one include provides "from_lib".
	incRoot := makeScope("/lib", nil, map[string]string{"from_lib": "imported"})
	incProject := &ir.Project{RootScope: incRoot}
	parent := &ir.Scope{
		Path: "/",
		Vars: make(map[string]*ir.Var),
		Includes: []*ir.Include{
			{Spec: "./lib.yml", Resolved: "/abs/lib.yml", Kind: ir.IncludeLocal, Project: incProject},
		},
	}

	v, found := Lookup(parent, "from_lib")
	if v == nil {
		t.Fatal("expected to find via include")
	}
	if v.Value != "imported" {
		t.Errorf("v.Value = %q, want %q", v.Value, "imported")
	}
	if found != incRoot {
		t.Errorf("found should be include's root scope; got %s", found.Path)
	}
}

func TestLookup_OwnVarBeatsInclude(t *testing.T) {
	// Both the scope and an include declare "x" — local wins.
	incRoot := makeScope("/lib", nil, map[string]string{"x": "from_lib"})
	incProject := &ir.Project{RootScope: incRoot}
	scope := &ir.Scope{
		Path:     "/",
		Vars:     map[string]*ir.Var{"x": {Name: "x", Value: "from_self"}},
		VarOrder: []string{"x"},
		Includes: []*ir.Include{
			{Spec: "./lib.yml", Project: incProject},
		},
	}

	v, found := Lookup(scope, "x")
	if v.Value != "from_self" {
		t.Errorf("expected own var to win; got %q", v.Value)
	}
	if found != scope {
		t.Error("found should be the local scope")
	}
}

func TestLookup_IncludeOrderMatters(t *testing.T) {
	// Two includes both declare "x"; first-declared wins.
	libA := makeScope("/libA", nil, map[string]string{"x": "from_A"})
	libB := makeScope("/libB", nil, map[string]string{"x": "from_B"})
	scope := &ir.Scope{
		Path: "/",
		Vars: make(map[string]*ir.Var),
		Includes: []*ir.Include{
			{Spec: "<A>", Project: &ir.Project{RootScope: libA}},
			{Spec: "<B>", Project: &ir.Project{RootScope: libB}},
		},
	}

	v, _ := Lookup(scope, "x")
	if v.Value != "from_A" {
		t.Errorf("expected first include to win; got %q", v.Value)
	}
}

func TestLookup_IncludeBeatsParent(t *testing.T) {
	// Parent declares "x", an include also declares it — include wins
	// because the walk checks includes before parent.
	parent := makeScope("/", nil, map[string]string{"x": "from_parent"})
	incRoot := makeScope("/lib", nil, map[string]string{"x": "from_include"})

	child := &ir.Scope{
		Path:   "/target",
		Parent: parent,
		Vars:   make(map[string]*ir.Var),
		Includes: []*ir.Include{
			{Spec: "<lib>", Project: &ir.Project{RootScope: incRoot}},
		},
	}

	v, found := Lookup(child, "x")
	if v.Value != "from_include" {
		t.Errorf("include should beat parent; got %q from %s", v.Value, found.Path)
	}
}

func TestLookup_TransitiveInclude(t *testing.T) {
	// Project A includes B, which includes C. Looking up from A's root
	// should find a var declared in C.
	cRoot := makeScope("/C", nil, map[string]string{"deep": "in_C"})
	bRoot := &ir.Scope{
		Path: "/B",
		Vars: make(map[string]*ir.Var),
		Includes: []*ir.Include{
			{Spec: "<C>", Project: &ir.Project{RootScope: cRoot}},
		},
	}
	aRoot := &ir.Scope{
		Path: "/A",
		Vars: make(map[string]*ir.Var),
		Includes: []*ir.Include{
			{Spec: "<B>", Project: &ir.Project{RootScope: bRoot}},
		},
	}

	v, found := Lookup(aRoot, "deep")
	if v == nil {
		t.Fatal("expected to find deep transitively")
	}
	if v.Value != "in_C" {
		t.Errorf("v.Value = %q, want %q", v.Value, "in_C")
	}
	if found != cRoot {
		t.Error("found should be C's scope")
	}
}

func TestLookup_CyclicScopeIsBounded(t *testing.T) {
	// Construct a malformed scope that's self-cyclic via includes.
	// Lookup should terminate, not stack-overflow.
	root := &ir.Scope{
		Path: "/",
		Vars: make(map[string]*ir.Var),
	}
	root.Includes = []*ir.Include{
		{Spec: "<cycle>", Project: &ir.Project{RootScope: root}},
	}

	v, _ := Lookup(root, "anything")
	if v != nil {
		t.Errorf("expected not-found for missing var in cyclic scope")
	}
	// Test passes if we don't stack-overflow.
}

func TestLookupSource_Direct(t *testing.T) {
	scope := makeScope("/", nil, map[string]string{"x": "1"})
	scope.Vars["x"].Source = ir.SourceLoc{File: "gmk.yml", Line: 5}

	src := LookupSource(scope, "x")
	if !strings.Contains(src, "declared at") {
		t.Errorf("source %q should mention 'declared at'", src)
	}
	if !strings.Contains(src, "gmk.yml:5") {
		t.Errorf("source %q should contain location", src)
	}
}

func TestLookupSource_Parent(t *testing.T) {
	parent := makeScope("/", nil, map[string]string{"x": "1"})
	child := makeScope("/child", parent, nil)

	src := LookupSource(child, "x")
	if !strings.Contains(src, "inherited from parent") {
		t.Errorf("source %q should mention parent", src)
	}
}

func TestLookupSource_Include(t *testing.T) {
	incRoot := makeScope("/lib", nil, map[string]string{"x": "1"})
	scope := &ir.Scope{
		Path: "/",
		Vars: make(map[string]*ir.Var),
		Includes: []*ir.Include{
			{Spec: "<lib>", Project: &ir.Project{RootScope: incRoot}},
		},
	}

	src := LookupSource(scope, "x")
	if !strings.Contains(src, "inherited from include") {
		t.Errorf("source %q should mention include", src)
	}
	if !strings.Contains(src, "/lib") {
		t.Errorf("source %q should contain include scope path", src)
	}
}

func TestLookupSource_NotFound(t *testing.T) {
	scope := makeScope("/", nil, nil)
	if got := LookupSource(scope, "missing"); got != "" {
		t.Errorf("expected empty for not-found, got %q", got)
	}
}
