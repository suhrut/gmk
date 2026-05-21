package resolve

import (
	"errors"
	"os"
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

// Note: Stage 2's TestValidVarName test was removed when the expression
// engine took over var-name validation. Identifier rules now live in
// internal/expr/lexer.go (isIdentStart, isIdentCont) and are exercised
// indirectly by every ${name} parse test in expr/parser_test.go.

// ============================================================================
// Stage 3a additions: expression language reachable through ResolveString.
// ============================================================================
//
// These tests exercise the new grammar paths end-to-end via the public
// resolve API. The expr package has its own focused tests for AST building
// and evaluation; here we verify the resolve <-> expr bridge works:
// scope-based var resolution, env/ctx providers, function dispatch, and
// error wrapping (ErrUndefined / ErrCycle / expr.ErrRequired).

func TestResolveString_TypedRef_EnvSet(t *testing.T) {
	t.Setenv("GMK_TEST_VAR", "from_env")
	p := newProjectWithVars(nil)
	got, err := ResolveString("env_value=${env:GMK_TEST_VAR}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "env_value=from_env" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_TypedRef_EnvUnsetIsEmpty(t *testing.T) {
	os.Unsetenv("GMK_UNSET_VAR_NEVER_EXISTS_999")
	p := newProjectWithVars(nil)
	got, err := ResolveString("[${env:GMK_UNSET_VAR_NEVER_EXISTS_999}]", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "[]" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_Modifier_Default(t *testing.T) {
	os.Unsetenv("GMK_DEFAULT_TEST_999")
	p := newProjectWithVars(nil)
	got, err := ResolveString("${env:GMK_DEFAULT_TEST_999:-fallback}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_Modifier_Required_FiresOnUndefined(t *testing.T) {
	p := newProjectWithVars(nil)
	_, err := ResolveString("${missing:?must be set}", p)
	if err == nil {
		t.Fatal("expected error")
	}
	// Error message should carry the user's "must be set" message.
	if !strings.Contains(err.Error(), "must be set") {
		t.Errorf("err should include user msg, got %v", err)
	}
}

func TestResolveString_Modifier_Required_PassesOnSet(t *testing.T) {
	p := newProjectWithVars(map[string]string{"x": "value"})
	got, err := ResolveString("${x:?must be set}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "value" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_Pipeline_Upper(t *testing.T) {
	p := newProjectWithVars(map[string]string{"name": "hello"})
	got, err := ResolveString("${name | upper}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "HELLO" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_Pipeline_Chain(t *testing.T) {
	p := newProjectWithVars(map[string]string{"name": "  hi  "})
	got, err := ResolveString("${name | trim | upper}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "HI" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_FuncCall(t *testing.T) {
	p := newProjectWithVars(map[string]string{"a": "hello world"})
	got, err := ResolveString(`${starts_with(a, "hello")}`, p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "true" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_Comparison(t *testing.T) {
	p := newProjectWithVars(map[string]string{"env": "prod"})
	got, err := ResolveString(`${env == "prod"}`, p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "true" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_MixedTextAndExpression(t *testing.T) {
	p := newProjectWithVars(map[string]string{"user": "gss"})
	got, err := ResolveString("hello, ${user | upper}! welcome.", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello, GSS! welcome." {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_VarTypedRef(t *testing.T) {
	// ${var:name} is explicit form of ${name}; both should resolve identically.
	p := newProjectWithVars(map[string]string{"x": "explicit"})
	got, err := ResolveString("${var:x}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "explicit" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_DeepNestedVars(t *testing.T) {
	p := newProjectWithVars(map[string]string{
		"a": "${b}",
		"b": "${c}",
		"c": "${d}",
		"d": "leaf",
	})
	got, err := ResolveString("${a}", p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "leaf" {
		t.Errorf("got %q", got)
	}
}

func TestResolveString_ErrorIncludesPositionContext(t *testing.T) {
	// When a missing var is encountered, the error should mention the
	// var name in a useful form.
	p := newProjectWithVars(nil)
	_, err := ResolveString("${missing_xyz}", p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing_xyz") {
		t.Errorf("err should include var name, got %v", err)
	}
}
