package expr_test

import (
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
)

// callRecorder captures every ResolveCall invocation so tests can
// verify the iteration variable is bound correctly and pinned args
// flow through on every iteration.
type callRecorder struct {
	name  string
	calls []map[string]expr.Value
	// returnFn lets a test customize what each call returns based on
	// the call args. If nil, returns NewString("ok") every time.
	returnFn func(args map[string]expr.Value) (expr.Value, error)
}

func (r *callRecorder) ResolveCall(name string, args map[string]expr.Value) (expr.Value, error) {
	r.name = name
	// Copy args so concurrent or later mutations by the caller don't
	// rewrite history. The eval path doesn't actually mutate, but
	// defensive copying makes the test failure modes legible.
	cp := make(map[string]expr.Value, len(args))
	for k, v := range args {
		cp[k] = v
	}
	r.calls = append(r.calls, cp)
	if r.returnFn != nil {
		return r.returnFn(args)
	}
	return expr.NewString("ok"), nil
}

func evalMapExpr(t *testing.T, src string, vars map[string]expr.Value, rec *callRecorder) expr.Value {
	t.Helper()
	node, err := expr.ParseTemplate(src, expr.Position{})
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	e := &expr.Evaluator{
		Vars:  mapVarResolver(vars),
		Calls: rec,
	}
	v, err := e.Eval(node)
	if err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	return v
}

// map over a 3-element list calls the function 3 times with item=
// bound to each element in order.
func TestMap_BindsItemPerCall(t *testing.T) {
	items := expr.NewList([]expr.Value{
		expr.NewString("a"),
		expr.NewString("b"),
		expr.NewString("c"),
	})
	rec := &callRecorder{}
	got := evalMapExpr(t, `${map:upper(items=letters)}`,
		map[string]expr.Value{"letters": items}, rec)

	if len(rec.calls) != 3 {
		t.Fatalf("want 3 calls, got %d", len(rec.calls))
	}
	if rec.name != "upper" {
		t.Errorf("dispatch name = %q, want 'upper'", rec.name)
	}
	for i, want := range []string{"a", "b", "c"} {
		if got := rec.calls[i]["item"].AsString(); got != want {
			t.Errorf("call[%d].item = %q, want %q", i, got, want)
		}
	}
	// Default callRecorder returns "ok" — result list should be 3 oks.
	if got.Kind != expr.ListKind || len(got.List) != 3 {
		t.Fatalf("result: %+v", got)
	}
	for i, v := range got.List {
		if v.AsString() != "ok" {
			t.Errorf("result[%d] = %q", i, v.AsString())
		}
	}
}

// pinned args (everything other than `items`) flow through unchanged
// on every iteration.
func TestMap_PinnedArgsPassedEveryCall(t *testing.T) {
	items := expr.NewList([]expr.Value{
		expr.NewInt(1),
		expr.NewInt(2),
	})
	rec := &callRecorder{}
	_ = evalMapExpr(t,
		`${map:format(items=nums, prefix="N=", base=10)}`,
		map[string]expr.Value{"nums": items}, rec)

	if len(rec.calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(rec.calls))
	}
	for i, c := range rec.calls {
		if c["prefix"].AsString() != "N=" {
			t.Errorf("call[%d].prefix = %q", i, c["prefix"].AsString())
		}
		if c["base"].Int != 10 {
			t.Errorf("call[%d].base = %v", i, c["base"].Int)
		}
	}
}

// map over a list of maps preserves nesting — item is bound to the
// whole inner map, accessible via field access in the called fn.
func TestMap_OverListOfMaps(t *testing.T) {
	services := expr.NewList([]expr.Value{
		expr.NewMap(map[string]expr.Value{
			"name": expr.NewString("api"),
			"port": expr.NewInt(8080),
		}),
		expr.NewMap(map[string]expr.Value{
			"name": expr.NewString("worker"),
			"port": expr.NewInt(9000),
		}),
	})
	rec := &callRecorder{}
	_ = evalMapExpr(t, `${map:render-svc(items=services)}`,
		map[string]expr.Value{"services": services}, rec)

	if len(rec.calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(rec.calls))
	}
	first := rec.calls[0]["item"]
	if first.Kind != expr.MapKind {
		t.Fatalf("first item should be Map, got %v", first.Kind)
	}
	if first.Map["name"].AsString() != "api" {
		t.Errorf("first.name = %q", first.Map["name"].AsString())
	}
}

// empty list yields empty result list — no error, no spurious calls.
func TestMap_EmptyListNoOp(t *testing.T) {
	rec := &callRecorder{}
	got := evalMapExpr(t, `${map:fn(items=empty)}`,
		map[string]expr.Value{"empty": expr.NewList(nil)}, rec)

	if len(rec.calls) != 0 {
		t.Errorf("want 0 calls, got %d", len(rec.calls))
	}
	if got.Kind != expr.ListKind || len(got.List) != 0 {
		t.Errorf("want empty list, got %+v", got)
	}
}

// filter: predicate returns Bool; result is items where it returned true.
// The KEPT item is the original element, not the predicate's true value.
func TestFilter_KeepsItemsMatchingPredicate(t *testing.T) {
	items := expr.NewList([]expr.Value{
		expr.NewInt(1),
		expr.NewInt(2),
		expr.NewInt(3),
		expr.NewInt(4),
	})
	rec := &callRecorder{
		// Return true for even numbers (item % 2 == 0).
		returnFn: func(args map[string]expr.Value) (expr.Value, error) {
			n := args["item"].Int
			return expr.NewBool(n%2 == 0), nil
		},
	}
	got := evalMapExpr(t, `${filter:is_even(items=nums)}`,
		map[string]expr.Value{"nums": items}, rec)

	if got.Kind != expr.ListKind {
		t.Fatalf("want list, got %+v", got)
	}
	if len(got.List) != 2 {
		t.Fatalf("want 2 items, got %d (%+v)", len(got.List), got.List)
	}
	if got.List[0].Int != 2 || got.List[1].Int != 4 {
		t.Errorf("filtered = %+v", got.List)
	}
}

// filter: predicate returning non-Bool errors with a clear message.
func TestFilter_NonBoolPredicateErrors(t *testing.T) {
	items := expr.NewList([]expr.Value{expr.NewInt(1)})
	rec := &callRecorder{
		returnFn: func(args map[string]expr.Value) (expr.Value, error) {
			return expr.NewString("not a bool"), nil
		},
	}
	node, err := expr.ParseTemplate(`${filter:bad(items=nums)}`, expr.Position{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e := &expr.Evaluator{
		Vars:  mapVarResolver(map[string]expr.Value{"nums": items}),
		Calls: rec,
	}
	_, err = e.Eval(node)
	if err == nil {
		t.Fatal("expected error for non-bool predicate result")
	}
	if !strings.Contains(err.Error(), "expected bool") {
		t.Errorf("error should mention expected bool, got: %v", err)
	}
}

// missing `items` arg → clear error pointing at the missing name.
func TestMap_MissingItemsErrors(t *testing.T) {
	rec := &callRecorder{}
	node, err := expr.ParseTemplate(`${map:fn(prefix="hi")}`, expr.Position{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e := &expr.Evaluator{Vars: mapVarResolver(nil), Calls: rec}
	_, err = e.Eval(node)
	if err == nil {
		t.Fatal("expected error for missing items")
	}
	if !strings.Contains(err.Error(), "items") {
		t.Errorf("error should mention `items`, got: %v", err)
	}
}

// `items` arg that's not a List → clear error stating the kind.
func TestMap_NonListItemsErrors(t *testing.T) {
	rec := &callRecorder{}
	node, err := expr.ParseTemplate(`${map:fn(items=s)}`, expr.Position{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e := &expr.Evaluator{
		Vars:  mapVarResolver(map[string]expr.Value{"s": expr.NewString("not a list")}),
		Calls: rec,
	}
	_, err = e.Eval(node)
	if err == nil {
		t.Fatal("expected error for non-list items")
	}
	if !strings.Contains(err.Error(), "must be a list") {
		t.Errorf("error should explain the type requirement, got: %v", err)
	}
}

// passing a pinned `item=` arg conflicts with the per-iteration binding
// — should error rather than silently let the user step on themselves.
func TestMap_ItemArgShadowErrors(t *testing.T) {
	items := expr.NewList([]expr.Value{expr.NewInt(1)})
	rec := &callRecorder{}
	node, err := expr.ParseTemplate(`${map:fn(items=L, item="oops")}`, expr.Position{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e := &expr.Evaluator{
		Vars:  mapVarResolver(map[string]expr.Value{"L": items}),
		Calls: rec,
	}
	_, err = e.Eval(node)
	if err == nil {
		t.Fatal("expected error for `item` shadow")
	}
	if !strings.Contains(err.Error(), "item") {
		t.Errorf("error should mention 'item' shadowing, got: %v", err)
	}
}

// map without a CallResolver errors clearly rather than panicking.
func TestMap_NoCallResolverErrors(t *testing.T) {
	items := expr.NewList([]expr.Value{expr.NewInt(1)})
	node, err := expr.ParseTemplate(`${map:fn(items=L)}`, expr.Position{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e := &expr.Evaluator{
		Vars: mapVarResolver(map[string]expr.Value{"L": items}),
		// no Calls
	}
	_, err = e.Eval(node)
	if err == nil {
		t.Fatal("expected error with no Calls resolver")
	}
	if !strings.Contains(err.Error(), "call resolver") {
		t.Errorf("error should mention call resolver, got: %v", err)
	}
}

// map composes with splat: splat a Map of pinned args into the call.
func TestMap_ComposesWithSplat(t *testing.T) {
	items := expr.NewList([]expr.Value{expr.NewInt(1), expr.NewInt(2)})
	pinned := expr.NewMap(map[string]expr.Value{
		"prefix": expr.NewString("P="),
		"base":   expr.NewInt(10),
	})
	rec := &callRecorder{}
	_ = evalMapExpr(t, `${map:fn(items=L, ...cfg)}`,
		map[string]expr.Value{"L": items, "cfg": pinned}, rec)

	if len(rec.calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(rec.calls))
	}
	if rec.calls[0]["prefix"].AsString() != "P=" {
		t.Errorf("splatted prefix didn't reach call: %+v", rec.calls[0])
	}
	if rec.calls[0]["base"].Int != 10 {
		t.Errorf("splatted base didn't reach call: %+v", rec.calls[0])
	}
}
