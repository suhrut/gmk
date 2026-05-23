package resolve

import (
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
)

// newScopeWithStructuredVar builds a scope holding one VarStructured
// alongside any scalar vars passed in. Saves a few lines per test.
func newScopeWithStructuredVar(name string, structured expr.Value, scalars map[string]string) *ir.Scope {
	s := newScopeWithVars("/", nil, scalars)
	s.Vars[name] = &ir.Var{
		Name:       name,
		Kind:       ir.VarStructured,
		Structured: structured,
	}
	s.VarOrder = append(s.VarOrder, name)
	return s
}

// Bare reference ${cfg} to a structured var resolves to its String form
// — for Maps and Lists that's their bracketed JSON-ish representation,
// which is good enough that downstream uses (echo, debugging) work.
func TestStructured_BareReferenceResolves(t *testing.T) {
	cfg := expr.NewMap(map[string]expr.Value{
		"host": expr.NewString("db.internal"),
		"port": expr.NewInt(5432),
	})
	sc := newScopeWithStructuredVar("cfg", cfg, nil)
	p := &ir.Project{
		SourcePath: "/test/gmk.yml",
		RootScope:  sc,
		Vars:       sc.Vars,
		VarOrder:   sc.VarOrder,
		Targets:    make(map[string]*ir.Target),
	}

	got, err := ResolveString("${cfg}", p)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Map.String() form is deterministic enough — contains both keys
	// in some order. The exact format is the Value's AsString.
	if !strings.Contains(got, "host") || !strings.Contains(got, "db.internal") {
		t.Errorf("got %q, expected to contain host/db.internal", got)
	}
}

// Index access ${cfg.host} returns the Map field as a String when the
// field is a String.
func TestStructured_IndexAccessString(t *testing.T) {
	cfg := expr.NewMap(map[string]expr.Value{
		"host": expr.NewString("db.internal"),
		"port": expr.NewInt(5432),
	})
	sc := newScopeWithStructuredVar("cfg", cfg, nil)
	p := &ir.Project{
		SourcePath: "/test/gmk.yml",
		RootScope:  sc,
		Vars:       sc.Vars,
		VarOrder:   sc.VarOrder,
		Targets:    make(map[string]*ir.Target),
	}

	got, err := ResolveString("server: ${cfg.host}", p)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "server: db.internal" {
		t.Errorf("got %q", got)
	}
}

// Index access on an Int field stringifies to the int's text form.
func TestStructured_IndexAccessInt(t *testing.T) {
	cfg := expr.NewMap(map[string]expr.Value{
		"port": expr.NewInt(5432),
	})
	sc := newScopeWithStructuredVar("cfg", cfg, nil)
	p := &ir.Project{
		SourcePath: "/test/gmk.yml",
		RootScope:  sc,
		Vars:       sc.Vars,
		VarOrder:   sc.VarOrder,
		Targets:    make(map[string]*ir.Target),
	}

	got, err := ResolveString("port=${cfg.port}", p)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "port=5432" {
		t.Errorf("got %q", got)
	}
}

// Nested map access: ${cfg.db.host} traverses two levels.
func TestStructured_NestedMapAccess(t *testing.T) {
	cfg := expr.NewMap(map[string]expr.Value{
		"db": expr.NewMap(map[string]expr.Value{
			"host": expr.NewString("inner.host"),
		}),
	})
	sc := newScopeWithStructuredVar("cfg", cfg, nil)
	p := &ir.Project{
		SourcePath: "/test/gmk.yml",
		RootScope:  sc,
		Vars:       sc.Vars,
		VarOrder:   sc.VarOrder,
		Targets:    make(map[string]*ir.Target),
	}

	got, err := ResolveString("${cfg.db.host}", p)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "inner.host" {
		t.Errorf("got %q", got)
	}
}

// List index access: ${servers[0]} returns the first element.
func TestStructured_ListIndexAccess(t *testing.T) {
	servers := expr.NewList([]expr.Value{
		expr.NewString("10.0.0.1"),
		expr.NewString("10.0.0.2"),
	})
	sc := newScopeWithStructuredVar("servers", servers, nil)
	p := &ir.Project{
		SourcePath: "/test/gmk.yml",
		RootScope:  sc,
		Vars:       sc.Vars,
		VarOrder:   sc.VarOrder,
		Targets:    make(map[string]*ir.Target),
	}

	got, err := ResolveString("first=${servers[0]} second=${servers[1]}", p)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "first=10.0.0.1 second=10.0.0.2" {
		t.Errorf("got %q", got)
	}
}

// Scalar and structured vars coexist in the same scope; structured
// resolution doesn't break scalar lookup.
func TestStructured_MixedScalarAndStructured(t *testing.T) {
	cfg := expr.NewMap(map[string]expr.Value{
		"host": expr.NewString("structured-host"),
	})
	sc := newScopeWithStructuredVar("cfg", cfg, map[string]string{
		"region": "us-west-2",
	})
	p := &ir.Project{
		SourcePath: "/test/gmk.yml",
		RootScope:  sc,
		Vars:       sc.Vars,
		VarOrder:   sc.VarOrder,
		Targets:    make(map[string]*ir.Target),
	}

	got, err := ResolveString("${region}/${cfg.host}", p)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "us-west-2/structured-host" {
		t.Errorf("got %q", got)
	}
}

// Index access into a missing field surfaces a clear error.
func TestStructured_MissingFieldErrors(t *testing.T) {
	cfg := expr.NewMap(map[string]expr.Value{
		"host": expr.NewString("h"),
	})
	sc := newScopeWithStructuredVar("cfg", cfg, nil)
	p := &ir.Project{
		SourcePath: "/test/gmk.yml",
		RootScope:  sc,
		Vars:       sc.Vars,
		VarOrder:   sc.VarOrder,
		Targets:    make(map[string]*ir.Target),
	}

	_, err := ResolveString("${cfg.no_such_field}", p)
	if err == nil {
		t.Fatal("expected error for missing field")
	}
}
