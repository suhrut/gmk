package expr_test

import (
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
)

// staticVars is a tiny VarResolver for tests. Stores a flat name->Value
// map and rejects unknown names with ErrUndefinedVar.
type staticVars struct {
	m map[string]expr.Value
}

func (s *staticVars) ResolveVar(name string) (expr.Value, error) {
	v, ok := s.m[name]
	if !ok {
		return expr.NewNone(), expr.ErrUndefinedVar
	}
	return v, nil
}

// fixedCallResolver dispatches to a static map of functions.
type fixedCallResolver struct {
	funcs map[string]func(args map[string]expr.Value) (expr.Value, error)
}

func (f *fixedCallResolver) ResolveCall(name string, args map[string]expr.Value) (expr.Value, error) {
	fn, ok := f.funcs[name]
	if !ok {
		return expr.NewNone(), expr.ErrUndefinedVar // good enough sentinel for tests
	}
	return fn(args)
}

func evalIt(t *testing.T, src string, vars *staticVars, calls expr.CallResolver) expr.Value {
	t.Helper()
	n, err := expr.ParseTemplate(src, expr.Position{File: "test", Line: 1, Col: 1})
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	e := &expr.Evaluator{Vars: vars, Funcs: expr.DefaultFuncs(), Calls: calls}
	v, err := e.Eval(n)
	if err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	return v
}

func TestEval_FieldAccess(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"user": expr.NewMap(map[string]expr.Value{
			"name":  expr.NewString("alice"),
			"email": expr.NewString("a@example.com"),
		}),
	}}
	v := evalIt(t, "${user.name}", vars, nil)
	if v.Str != "alice" {
		t.Errorf("got %q, want alice", v.Str)
	}
}

func TestEval_NestedField(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"user": expr.NewMap(map[string]expr.Value{
			"profile": expr.NewMap(map[string]expr.Value{
				"city": expr.NewString("Bengaluru"),
			}),
		}),
	}}
	v := evalIt(t, "${user.profile.city}", vars, nil)
	if v.Str != "Bengaluru" {
		t.Errorf("got %q, want Bengaluru", v.Str)
	}
}

func TestEval_FieldAccessOnMissing(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"user": expr.NewMap(map[string]expr.Value{"name": expr.NewString("a")}),
	}}
	n, _ := expr.ParseTemplate("${user.unknown}", expr.Position{})
	e := &expr.Evaluator{Vars: vars, Funcs: expr.DefaultFuncs()}
	_, err := e.Eval(n)
	if err == nil {
		t.Fatal("expected error for missing field")
	}
}

func TestEval_IndexAccess_List(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"hosts": expr.NewList([]expr.Value{
			expr.NewString("a"), expr.NewString("b"), expr.NewString("c"),
		}),
	}}
	cases := []struct {
		src  string
		want string
	}{
		{"${hosts[0]}", "a"},
		{"${hosts[2]}", "c"},
		{"${hosts[-1]}", "c"},
	}
	for _, tc := range cases {
		v := evalIt(t, tc.src, vars, nil)
		if v.Str != tc.want {
			t.Errorf("%s: got %q, want %q", tc.src, v.Str, tc.want)
		}
	}
}

func TestEval_IndexAccess_Map(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"cfg": expr.NewMap(map[string]expr.Value{
			"host": expr.NewString("db.local"),
			"port": expr.NewInt(5432),
		}),
	}}
	v := evalIt(t, `${cfg["host"]}`, vars, nil)
	if v.Str != "db.local" {
		t.Errorf("got %q, want db.local", v.Str)
	}
	v = evalIt(t, `${cfg["port"]}`, vars, nil)
	if v.Int != 5432 {
		t.Errorf("got %d, want 5432", v.Int)
	}
}

func TestEval_IndexAccess_VarKey(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"cfg": expr.NewMap(map[string]expr.Value{
			"host": expr.NewString("api"),
		}),
		"k": expr.NewString("host"),
	}}
	v := evalIt(t, "${cfg[k]}", vars, nil)
	if v.Str != "api" {
		t.Errorf("got %q, want api", v.Str)
	}
}

func TestEval_ChainedFieldAndIndex(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"cluster": expr.NewMap(map[string]expr.Value{
			"hosts": expr.NewList([]expr.Value{
				expr.NewMap(map[string]expr.Value{
					"name": expr.NewString("h1"),
					"port": expr.NewInt(80),
				}),
				expr.NewMap(map[string]expr.Value{
					"name": expr.NewString("h2"),
					"port": expr.NewInt(81),
				}),
			}),
		}),
	}}
	v := evalIt(t, "${cluster.hosts[0].name}", vars, nil)
	if v.Str != "h1" {
		t.Errorf("got %q, want h1", v.Str)
	}
	v = evalIt(t, "${cluster.hosts[1].port}", vars, nil)
	if v.Int != 81 {
		t.Errorf("got %d, want 81", v.Int)
	}
}

func TestEval_NamedCall_PositionalArgs(t *testing.T) {
	calls := &fixedCallResolver{funcs: map[string]func(map[string]expr.Value) (expr.Value, error){
		"sum": func(args map[string]expr.Value) (expr.Value, error) {
			a, _ := args["0"].AsInt()
			b, _ := args["1"].AsInt()
			return expr.NewInt(a + b), nil
		},
	}}
	v := evalIt(t, "${call:sum(3, 4)}", nil, calls)
	if v.Int != 7 {
		t.Errorf("got %d, want 7", v.Int)
	}
}

func TestEval_NamedCall_NamedArgs(t *testing.T) {
	calls := &fixedCallResolver{funcs: map[string]func(map[string]expr.Value) (expr.Value, error){
		"build": func(args map[string]expr.Value) (expr.Value, error) {
			return expr.NewString(args["target"].Str + "-" + args["arch"].Str), nil
		},
	}}
	v := evalIt(t, `${call:build(target="api", arch="amd64")}`, nil, calls)
	if v.Str != "api-amd64" {
		t.Errorf("got %q, want api-amd64", v.Str)
	}
}

func TestEval_NamedCall_NoCalls(t *testing.T) {
	// No CallResolver configured.
	n, _ := expr.ParseTemplate("${call:f()}", expr.Position{})
	e := &expr.Evaluator{Funcs: expr.DefaultFuncs()}
	_, err := e.Eval(n)
	if err == nil || !strings.Contains(err.Error(), "no call resolver") {
		t.Errorf("expected 'no call resolver' error, got %v", err)
	}
}

func TestEval_NamedCall_ReturnsMap_FieldAccessChain(t *testing.T) {
	// Result of a call can be navigated into.
	calls := &fixedCallResolver{funcs: map[string]func(map[string]expr.Value) (expr.Value, error){
		"get-config": func(map[string]expr.Value) (expr.Value, error) {
			return expr.NewMap(map[string]expr.Value{
				"database": expr.NewMap(map[string]expr.Value{
					"host": expr.NewString("primary"),
				}),
			}), nil
		},
	}}
	v := evalIt(t, "${call:get-config().database.host}", nil, calls)
	if v.Str != "primary" {
		t.Errorf("got %q, want primary", v.Str)
	}
}

func TestEval_Builtin_Keys(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"m": expr.NewMap(map[string]expr.Value{
			"zebra": expr.NewInt(1),
			"alpha": expr.NewInt(2),
		}),
	}}
	v := evalIt(t, "${keys(m)}", vars, nil)
	if v.Kind != expr.ListKind {
		t.Fatalf("kind=%v", v.Kind)
	}
	if len(v.List) != 2 {
		t.Fatalf("len=%d", len(v.List))
	}
	if v.List[0].Str != "alpha" || v.List[1].Str != "zebra" {
		t.Errorf("keys not sorted: %v", v.List)
	}
}

func TestEval_Builtin_Values(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"m": expr.NewMap(map[string]expr.Value{
			"b": expr.NewString("second"),
			"a": expr.NewString("first"),
		}),
	}}
	v := evalIt(t, "${values(m)}", vars, nil)
	if len(v.List) != 2 {
		t.Fatalf("len=%d", len(v.List))
	}
	// Values follow key order, so alphabetical by key.
	if v.List[0].Str != "first" || v.List[1].Str != "second" {
		t.Errorf("got %v", v.List)
	}
}

func TestEval_Builtin_FirstLast(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"items": expr.NewList([]expr.Value{
			expr.NewString("a"), expr.NewString("b"), expr.NewString("c"),
		}),
	}}
	if v := evalIt(t, "${first(items)}", vars, nil); v.Str != "a" {
		t.Errorf("first: %q", v.Str)
	}
	if v := evalIt(t, "${last(items)}", vars, nil); v.Str != "c" {
		t.Errorf("last: %q", v.Str)
	}
}

func TestEval_Builtin_ToJSON(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"cfg": expr.NewMap(map[string]expr.Value{
			"port": expr.NewInt(8080),
			"name": expr.NewString("api"),
		}),
	}}
	v := evalIt(t, "${to_json(cfg)}", vars, nil)
	if v.Kind != expr.StringKind {
		t.Fatalf("kind=%v", v.Kind)
	}
	// Order is not deterministic in to_json since json.Encoder doesn't sort.
	// Accept either ordering.
	if !(v.Str == `{"name":"api","port":8080}` || v.Str == `{"port":8080,"name":"api"}`) {
		t.Errorf("to_json output: %q", v.Str)
	}

	// Scalars too.
	if v := evalIt(t, `${to_json("hello")}`, vars, nil); v.Str != `"hello"` {
		t.Errorf("to_json string: %q", v.Str)
	}
	if v := evalIt(t, "${to_json(42)}", vars, nil); v.Str != "42" {
		t.Errorf("to_json int: %q", v.Str)
	}
}

func TestEval_Builtin_FromJSON(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"raw": expr.NewString(`{"port": 8080, "name": "api"}`),
	}}
	v := evalIt(t, "${from_json(raw)}", vars, nil)
	if v.Kind != expr.MapKind {
		t.Fatalf("kind=%v", v.Kind)
	}
	if v.Map["port"].Kind != expr.IntKind || v.Map["port"].Int != 8080 {
		t.Errorf("port: %+v", v.Map["port"])
	}
	if v.Map["name"].Str != "api" {
		t.Errorf("name: %+v", v.Map["name"])
	}
}

func TestEval_Builtin_JSONRoundtrip(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"original": expr.NewMap(map[string]expr.Value{
			"id":    expr.NewInt(42),
			"tags":  expr.NewList([]expr.Value{expr.NewString("a"), expr.NewString("b")}),
			"flag":  expr.NewBool(true),
		}),
	}}
	v := evalIt(t, "${from_json(to_json(original))}", vars, nil)
	if v.Kind != expr.MapKind {
		t.Fatalf("kind=%v", v.Kind)
	}
	if v.Map["id"].Int != 42 {
		t.Errorf("id: %+v", v.Map["id"])
	}
	if !v.Map["flag"].Bool {
		t.Errorf("flag: %+v", v.Map["flag"])
	}
	if len(v.Map["tags"].List) != 2 {
		t.Errorf("tags len: %d", len(v.Map["tags"].List))
	}
}

func TestEval_Builtin_LenOnMap(t *testing.T) {
	vars := &staticVars{m: map[string]expr.Value{
		"m": expr.NewMap(map[string]expr.Value{
			"a": expr.NewInt(1),
			"b": expr.NewInt(2),
			"c": expr.NewInt(3),
		}),
	}}
	v := evalIt(t, "${len(m)}", vars, nil)
	if v.Int != 3 {
		t.Errorf("len(map): got %d, want 3", v.Int)
	}
}

func TestEval_PipelineWithKeys(t *testing.T) {
	// keys returns a list; join joins it. Composes via pipeline.
	vars := &staticVars{m: map[string]expr.Value{
		"m": expr.NewMap(map[string]expr.Value{
			"alpha": expr.NewInt(1),
			"beta":  expr.NewInt(2),
		}),
	}}
	v := evalIt(t, `${m | keys | join(",")}`, vars, nil)
	if v.Str != "alpha,beta" {
		t.Errorf("got %q, want alpha,beta", v.Str)
	}
}
