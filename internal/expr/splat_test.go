package expr_test

import (
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
)

// reuseStubRender wraps a stubRenderResolver so we can capture what
// args the dispatcher actually received after splat expansion.
type splatStub struct {
	lastArgs map[string]expr.Value
}

func (s *splatStub) ResolveRender(_ string, args map[string]expr.Value) (expr.Value, error) {
	s.lastArgs = args
	return expr.NewString("ok"), nil
}

func evalWithMap(t *testing.T, src string, mapVal expr.Value) map[string]expr.Value {
	t.Helper()
	node, err := expr.ParseTemplate(src, expr.Position{})
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	stub := &splatStub{}
	vars := map[string]expr.Value{"cfg": mapVal}
	if mapVal.Kind == expr.MapKind {
		// Also expose other names some tests use.
		vars["defaults"] = mapVal
		vars["overrides"] = mapVal
	}
	e := &expr.Evaluator{
		Vars:    mapVarResolver(vars),
		Renders: stub,
	}
	if _, err := e.Eval(node); err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	return stub.lastArgs
}

func TestEval_Splat_SpreadsMapKeysAsArgs(t *testing.T) {
	cfg := expr.NewMap(map[string]expr.Value{
		"image":    expr.NewString("myorg/api"),
		"version":  expr.NewString("1.22"),
		"replicas": expr.NewInt(3),
	})
	got := evalWithMap(t, `${render:k8s(...cfg)}`, cfg)

	if got["image"].AsString() != "myorg/api" {
		t.Errorf("image=%q, want myorg/api", got["image"].AsString())
	}
	if got["version"].AsString() != "1.22" {
		t.Errorf("version=%q", got["version"].AsString())
	}
	if got["replicas"].AsString() != "3" {
		t.Errorf("replicas=%q", got["replicas"].AsString())
	}
}

func TestEval_Splat_NamedArgsOverrideSplat(t *testing.T) {
	// Standard left-to-right with named-wins-on-conflict. The user
	// expects: splat in defaults, override one field by name.
	cfg := expr.NewMap(map[string]expr.Value{
		"image":   expr.NewString("default-image"),
		"version": expr.NewString("default-version"),
	})
	got := evalWithMap(t, `${render:k8s(...cfg, version="overridden")}`, cfg)

	if got["image"].AsString() != "default-image" {
		t.Errorf("image should come from splat, got %q", got["image"].AsString())
	}
	if got["version"].AsString() != "overridden" {
		t.Errorf("version should be overridden by named arg, got %q", got["version"].AsString())
	}
}

func TestEval_Splat_TwoSplatsLaterWins(t *testing.T) {
	// `${render:t(...a, ...b)}` — entries from b override entries from a.
	a := expr.NewMap(map[string]expr.Value{
		"shared": expr.NewString("from-a"),
		"only_a": expr.NewString("a-only"),
	})
	b := expr.NewMap(map[string]expr.Value{
		"shared": expr.NewString("from-b"),
		"only_b": expr.NewString("b-only"),
	})
	node, err := expr.ParseTemplate(`${render:t(...defaults, ...overrides)}`, expr.Position{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	stub := &splatStub{}
	e := &expr.Evaluator{
		Vars: mapVarResolver(map[string]expr.Value{
			"defaults":  a,
			"overrides": b,
		}),
		Renders: stub,
	}
	if _, err := e.Eval(node); err != nil {
		t.Fatalf("eval: %v", err)
	}
	if stub.lastArgs["shared"].AsString() != "from-b" {
		t.Errorf("shared should be from later splat, got %q", stub.lastArgs["shared"].AsString())
	}
	if stub.lastArgs["only_a"].AsString() != "a-only" || stub.lastArgs["only_b"].AsString() != "b-only" {
		t.Errorf("unique entries should survive: got %v", stub.lastArgs)
	}
}

func TestEval_Splat_OnNonMapErrors(t *testing.T) {
	// Splatting a non-Map must surface a clear error, not silently
	// produce garbage args.
	node, err := expr.ParseTemplate(`${render:t(...cfg)}`, expr.Position{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e := &expr.Evaluator{
		Vars: mapVarResolver(map[string]expr.Value{
			"cfg": expr.NewString("not a map"),
		}),
		Renders: &splatStub{},
	}
	_, err = e.Eval(node)
	if err == nil {
		t.Fatal("expected error splatting a string")
	}
	if !strings.Contains(err.Error(), "splat") {
		t.Errorf("error should mention splat, got: %v", err)
	}
	if !strings.Contains(err.Error(), "must be a map") {
		t.Errorf("error should explain the requirement, got: %v", err)
	}
}

func TestEval_Splat_CombinesWithCallKind(t *testing.T) {
	// Splat works for ${call:...} too, not just ${render:...}. The
	// dispatch happens after arg collection, so the splat handling
	// is kind-agnostic.
	cfg := expr.NewMap(map[string]expr.Value{"x": expr.NewInt(1), "y": expr.NewInt(2)})
	node, err := expr.ParseTemplate(`${call:fn(...cfg)}`, expr.Position{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var got map[string]expr.Value
	calls := stubCallResolver(func(name string, args map[string]expr.Value) (expr.Value, error) {
		got = args
		return expr.NewString("ok"), nil
	})
	e := &expr.Evaluator{
		Vars:  mapVarResolver(map[string]expr.Value{"cfg": cfg}),
		Calls: calls,
	}
	if _, err := e.Eval(node); err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got["x"].AsString() != "1" || got["y"].AsString() != "2" {
		t.Errorf("got %v", got)
	}
}

func TestEval_Splat_EmptyMapIsNoOp(t *testing.T) {
	// Edge case: splatting an empty map should leave args empty
	// (no spurious entries, no error).
	empty := expr.NewMap(map[string]expr.Value{})
	got := evalWithMap(t, `${render:t(...cfg)}`, empty)
	if len(got) != 0 {
		t.Errorf("splatting empty map should leave args empty, got %d entries: %v",
			len(got), got)
	}
}

func TestEval_Splat_StillRecognisesPositional(t *testing.T) {
	// Positional args before splat should still get "0", "1" indices.
	// This guards the parser's bookkeeping.
	cfg := expr.NewMap(map[string]expr.Value{"k": expr.NewString("v")})
	got := evalWithMap(t, `${render:t("first", ...cfg)}`, cfg)
	if got["0"].AsString() != "first" {
		t.Errorf("positional arg 0 should be 'first', got %q", got["0"].AsString())
	}
	if got["k"].AsString() != "v" {
		t.Errorf("splat key should be present, got %v", got)
	}
}

// stubCallResolver is a function wrapper around CallResolver for terse tests.
type stubCallResolver func(name string, args map[string]expr.Value) (expr.Value, error)

func (s stubCallResolver) ResolveCall(name string, args map[string]expr.Value) (expr.Value, error) {
	return s(name, args)
}
