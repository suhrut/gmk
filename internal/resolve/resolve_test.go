package resolve

import (
	"errors"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/ir"
)

// --- helpers ---

func newScopeWithVars(path string, parent *ir.Scope, vars map[string]string) *ir.Scope {
	s := &ir.Scope{
		Path:     path,
		Parent:   parent,
		Vars:     make(map[string]*ir.Var, len(vars)),
		VarOrder: make([]string, 0, len(vars)),
	}
	for k, v := range vars {
		s.Vars[k] = &ir.Var{Name: k, Value: v}
		s.VarOrder = append(s.VarOrder, k)
	}
	return s
}

func newProjectWithVars(vars map[string]string) *ir.Project {
	root := newScopeWithVars("/", nil, vars)
	p := &ir.Project{
		SourcePath: "/test/build.yml",
		Root:       "/test",
		RootScope:  root,
		Vars:       root.Vars,
		VarOrder:   root.VarOrder,
		Targets:    make(map[string]*ir.Target),
	}
	return p
}

// --- Stage 1 API: backwards compat ---

func TestResolveString_Plain(t *testing.T) {
	p := newProjectWithVars(map[string]string{"x": "hello"})
	got, err := ResolveString("just text, no refs", p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "just text, no refs"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveString_SingleRef(t *testing.T) {
	p := newProjectWithVars(map[string]string{"name": "world"})
	got, err := ResolveString("hello ${name}!", p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "hello world!"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveString_NestedRef(t *testing.T) {
	p := newProjectWithVars(map[string]string{"a": "${b}", "b": "deep"})
	got, err := ResolveString("value=${a}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "value=deep" {
		t.Errorf("got %q, want %q", got, "value=deep")
	}
}

func TestResolveString_DollarEscape(t *testing.T) {
	p := newProjectWithVars(map[string]string{"x": "value"})
	got, err := ResolveString("literal $$ then ${x}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "literal $ then value" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_Cycle(t *testing.T) {
	p := newProjectWithVars(map[string]string{"a": "${b}", "b": "${a}"})
	_, err := ResolveString("${a}", p)
	if err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Errorf("expected cyclic error, got %v", err)
	}
}

func TestResolveString_Undefined(t *testing.T) {
	p := newProjectWithVars(map[string]string{})
	_, err := ResolveString("${missing}", p)
	if !errors.Is(err, ErrUndefined) {
		t.Errorf("err = %v, want wrapping ErrUndefined", err)
	}
}

func TestResolveString_NilProject(t *testing.T) {
	_, err := ResolveString("text", nil)
	if err == nil {
		t.Fatal("expected error for nil project")
	}
}

func TestResolve_Var(t *testing.T) {
	p := newProjectWithVars(map[string]string{
		"greeting": "hello",
		"who":      "${greeting} world",
	})
	got, err := Resolve("who", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("got %q", got)
	}
}

// --- Stage 2 API: scope-aware ---

func TestResolveInScope_Direct(t *testing.T) {
	sc := newScopeWithVars("/", nil, map[string]string{"x": "v"})
	got, err := ResolveInScope("x", sc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v" {
		t.Errorf("got %q", got)
	}
}

func TestResolveInScope_InheritedFromParent(t *testing.T) {
	parent := newScopeWithVars("/", nil, map[string]string{"shared": "from_parent"})
	child := newScopeWithVars("/child", parent, nil)

	got, err := ResolveInScope("shared", child)
	if err != nil {
		t.Fatal(err)
	}
	if got != "from_parent" {
		t.Errorf("got %q", got)
	}
}

func TestResolveInScope_ChildOverrides(t *testing.T) {
	parent := newScopeWithVars("/", nil, map[string]string{"x": "parent_val"})
	child := newScopeWithVars("/c", parent, map[string]string{"x": "child_val"})

	got, _ := ResolveInScope("x", child)
	if got != "child_val" {
		t.Errorf("got %q", got)
	}
}

func TestResolveInScope_ViaInclude(t *testing.T) {
	incRoot := newScopeWithVars("/lib", nil, map[string]string{"from_lib": "imported"})
	incProject := &ir.Project{RootScope: incRoot}
	sc := &ir.Scope{
		Path: "/",
		Vars: make(map[string]*ir.Var),
		Includes: []*ir.Include{
			{Spec: "<lib>", Project: incProject},
		},
	}

	got, err := ResolveInScope("from_lib", sc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "imported" {
		t.Errorf("got %q", got)
	}
}

func TestResolveStringInScope_RefAcrossInclude(t *testing.T) {
	incRoot := newScopeWithVars("/lib", nil, map[string]string{"base": "X"})
	incProject := &ir.Project{RootScope: incRoot}
	sc := &ir.Scope{
		Path:     "/",
		Vars:     map[string]*ir.Var{"local": {Name: "local", Value: "${base}-Y"}},
		VarOrder: []string{"local"},
		Includes: []*ir.Include{
			{Spec: "<lib>", Project: incProject},
		},
	}

	got, err := ResolveStringInScope("${local}", sc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "X-Y" {
		t.Errorf("got %q, want X-Y", got)
	}
}

func TestResolveInScope_NilScope(t *testing.T) {
	_, err := ResolveInScope("x", nil)
	if !errors.Is(err, ErrUndefined) {
		t.Errorf("err = %v, want wrapping ErrUndefined", err)
	}
}

func TestResolveInScope_Undefined(t *testing.T) {
	sc := newScopeWithVars("/", nil, nil)
	_, err := ResolveInScope("missing", sc)
	if !errors.Is(err, ErrUndefined) {
		t.Errorf("err = %v, want wrapping ErrUndefined", err)
	}
}

func TestValidVarName(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"x", true}, {"_", true}, {"foo", true}, {"_foo", true},
		{"foo_bar", true}, {"FOO_BAR_123", true},
		{"", false}, {"1foo", false}, {"foo-bar", false},
		{"foo.bar", false}, {"foo bar", false},
	}
	for _, tc := range cases {
		if got := validVarName(tc.s); got != tc.want {
			t.Errorf("validVarName(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}
