package expr_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
)

// stubRenderResolver records what it was called with and returns a
// preprogrammed value. Lets us verify the expr layer's contract with
// the resolver without dragging in the template package.
type stubRenderResolver struct {
	lastName string
	lastArgs map[string]expr.Value
	out      expr.Value
	err      error
}

func (s *stubRenderResolver) ResolveRender(name string, args map[string]expr.Value) (expr.Value, error) {
	s.lastName = name
	s.lastArgs = args
	return s.out, s.err
}

func evalRender(t *testing.T, src string, vars map[string]expr.Value, r expr.RenderResolver) expr.Value {
	t.Helper()
	node, err := expr.ParseTemplate(src, expr.Position{})
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	e := &expr.Evaluator{
		Vars:    mapVarResolver(vars),
		Renders: r,
	}
	v, err := e.Eval(node)
	if err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	return v
}

// mapVarResolver is a trivial VarResolver wrapping a map[string]Value.
// Used by the render tests since some templates reference vars in args.
type mapVarResolver map[string]expr.Value

func (m mapVarResolver) ResolveVar(name string) (expr.Value, error) {
	v, ok := m[name]
	if !ok {
		return expr.NewNone(), expr.ErrUndefinedVar
	}
	return v, nil
}

func TestEval_Render_BasicDispatch(t *testing.T) {
	stub := &stubRenderResolver{out: expr.NewString("Dockerfile content here")}
	v := evalRender(t, `${render:dockerfile-go(version="1.22", binary="myapp")}`, nil, stub)

	if v.AsString() != "Dockerfile content here" {
		t.Errorf("got %q, want %q", v.AsString(), "Dockerfile content here")
	}
	if stub.lastName != "dockerfile-go" {
		t.Errorf("template name received as %q, want dockerfile-go", stub.lastName)
	}
	// Named args must arrive in the args map under their names.
	if stub.lastArgs["version"].AsString() != "1.22" {
		t.Errorf("version arg: got %q", stub.lastArgs["version"].AsString())
	}
	if stub.lastArgs["binary"].AsString() != "myapp" {
		t.Errorf("binary arg: got %q", stub.lastArgs["binary"].AsString())
	}
}

func TestEval_Render_PositionalArgs(t *testing.T) {
	// Positional args end up keyed by stringified index ("0", "1", ...)
	// — same contract as call:.
	stub := &stubRenderResolver{out: expr.NewString("ok")}
	_ = evalRender(t, `${render:tmpl("alpha", "beta")}`, nil, stub)

	if stub.lastArgs["0"].AsString() != "alpha" {
		t.Errorf(`positional arg "0" got %q, want alpha`, stub.lastArgs["0"].AsString())
	}
	if stub.lastArgs["1"].AsString() != "beta" {
		t.Errorf(`positional arg "1" got %q, want beta`, stub.lastArgs["1"].AsString())
	}
}

func TestEval_Render_ArgsAreEvaluatedExpressions(t *testing.T) {
	// Args go through normal expression evaluation. Bare identifiers
	// inside the (k=v) list become VarRefs that resolve from the
	// var scope — same contract as call: args.
	stub := &stubRenderResolver{out: expr.NewString("ok")}
	vars := map[string]expr.Value{"version": expr.NewString("1.22")}
	_ = evalRender(t, `${render:tmpl(v=version)}`, vars, stub)

	if stub.lastArgs["v"].AsString() != "1.22" {
		t.Errorf("expected version VarRef expanded to 1.22, got %q", stub.lastArgs["v"].AsString())
	}
}

func TestEval_Render_NoResolverIsErrorWithGuidance(t *testing.T) {
	node, err := expr.ParseTemplate(`${render:foo()}`, expr.Position{})
	if err != nil {
		t.Fatal(err)
	}
	e := &expr.Evaluator{} // no Renders configured
	_, err = e.Eval(node)
	if err == nil {
		t.Fatal("expected error when Renders is nil")
	}
	if !strings.Contains(err.Error(), "no render resolver") {
		t.Errorf("error should mention missing render resolver, got: %v", err)
	}
}

func TestEval_Render_ResolverErrorPropagates(t *testing.T) {
	stub := &stubRenderResolver{err: errors.New("template not found")}
	node, _ := expr.ParseTemplate(`${render:missing()}`, expr.Position{})
	e := &expr.Evaluator{Renders: stub}
	_, err := e.Eval(node)
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if !strings.Contains(err.Error(), "template not found") {
		t.Errorf("inner error should propagate, got: %v", err)
	}
	if !strings.Contains(err.Error(), "render:missing") {
		t.Errorf("error should mention render:missing context, got: %v", err)
	}
}

func TestEval_Render_UnknownKindStillErrors(t *testing.T) {
	// After adding "render" as a recognised kind, "secret:" (or any
	// other unknown kind) must continue to error out predictably so
	// future-stage extensions fail fast.
	node, err := expr.ParseTemplate(`${secret:db-password()}`, expr.Position{})
	if err != nil {
		t.Fatal(err)
	}
	e := &expr.Evaluator{}
	_, err = e.Eval(node)
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
	if !strings.Contains(err.Error(), "unsupported call kind") {
		t.Errorf("error should say unsupported call kind, got: %v", err)
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("error should name the unknown kind, got: %v", err)
	}
	// And the suggestion list should mention both recognised kinds.
	if !strings.Contains(err.Error(), "call") || !strings.Contains(err.Error(), "render") {
		t.Errorf("error should list recognised kinds, got: %v", err)
	}
}
