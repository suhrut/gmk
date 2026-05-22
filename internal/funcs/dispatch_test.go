package funcs_test

import (
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/funcs"
	"github.com/suhrut/gmk/internal/ir"
)

// helper: build a Function with two params and a stub runner that
// echoes the bound args.
func twoParamFn(name string) *ir.Function {
	return &ir.Function{
		Name: name,
		Params: []ir.FunctionParam{
			{Name: "host", Type: "string"},
			{Name: "port", Type: "int", Default: defaultExpr("8080")},
		},
		Run: "echo x",
	}
}

func defaultExpr(s string) *expr.Node {
	n, _ := expr.ParseTemplate(s, expr.Position{File: "test", Line: 1, Col: 1})
	return &n
}

func echoRunner(fn *ir.Function, args map[string]expr.Value) (expr.Value, error) {
	// Return the bound args as a map so tests can inspect them.
	m := make(map[string]expr.Value, len(args))
	for k, v := range args {
		m[k] = v
	}
	return expr.NewMap(m), nil
}

func TestDispatcher_PositionalArgs(t *testing.T) {
	fn := twoParamFn("connect")
	d := funcs.New(map[string]*ir.Function{"connect": fn}, echoRunner)
	v, err := d.ResolveCall("connect", map[string]expr.Value{
		"0": expr.NewString("db.local"),
		"1": expr.NewInt(5432),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Map["host"].Str != "db.local" {
		t.Errorf("host: %+v", v.Map["host"])
	}
	if v.Map["port"].Int != 5432 {
		t.Errorf("port: %+v", v.Map["port"])
	}
}

func TestDispatcher_NamedArgs(t *testing.T) {
	fn := twoParamFn("connect")
	d := funcs.New(map[string]*ir.Function{"connect": fn}, echoRunner)
	v, err := d.ResolveCall("connect", map[string]expr.Value{
		"host": expr.NewString("api"),
		"port": expr.NewInt(80),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Map["host"].Str != "api" {
		t.Errorf("host: %+v", v.Map["host"])
	}
}

func TestDispatcher_MissingOptional_UsesDefault(t *testing.T) {
	fn := twoParamFn("connect")
	d := funcs.New(map[string]*ir.Function{"connect": fn}, echoRunner)
	v, err := d.ResolveCall("connect", map[string]expr.Value{
		"host": expr.NewString("api"),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Map["port"].Int != 8080 {
		t.Errorf("port should be default 8080: %+v", v.Map["port"])
	}
}

func TestDispatcher_MissingRequired_Errors(t *testing.T) {
	fn := twoParamFn("connect")
	d := funcs.New(map[string]*ir.Function{"connect": fn}, echoRunner)
	_, err := d.ResolveCall("connect", map[string]expr.Value{}) // no host
	if err == nil || !strings.Contains(err.Error(), "required param") {
		t.Errorf("expected required-param error, got %v", err)
	}
}

func TestDispatcher_ExtraArg_Errors(t *testing.T) {
	fn := twoParamFn("connect")
	d := funcs.New(map[string]*ir.Function{"connect": fn}, echoRunner)
	_, err := d.ResolveCall("connect", map[string]expr.Value{
		"host":  expr.NewString("api"),
		"port":  expr.NewInt(80),
		"extra": expr.NewString("oops"),
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Errorf("expected unexpected-arg error, got %v", err)
	}
}

func TestDispatcher_TypeCoercion(t *testing.T) {
	fn := &ir.Function{
		Name: "f",
		Params: []ir.FunctionParam{
			{Name: "n", Type: "int"},
		},
		Run: "echo",
	}
	d := funcs.New(map[string]*ir.Function{"f": fn}, echoRunner)
	v, err := d.ResolveCall("f", map[string]expr.Value{
		"0": expr.NewString("42"), // string -> int coercion
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Map["n"].Kind != expr.IntKind || v.Map["n"].Int != 42 {
		t.Errorf("coerced n: %+v", v.Map["n"])
	}
}

func TestDispatcher_TypeCoercionFails(t *testing.T) {
	fn := &ir.Function{
		Name:   "f",
		Params: []ir.FunctionParam{{Name: "n", Type: "int"}},
		Run:    "echo",
	}
	d := funcs.New(map[string]*ir.Function{"f": fn}, echoRunner)
	_, err := d.ResolveCall("f", map[string]expr.Value{
		"0": expr.NewString("not-a-number"),
	})
	if err == nil || !strings.Contains(err.Error(), "param") {
		t.Errorf("expected coercion error, got %v", err)
	}
}

func TestDispatcher_PositionalAndNamedOverlap_Errors(t *testing.T) {
	fn := twoParamFn("connect")
	d := funcs.New(map[string]*ir.Function{"connect": fn}, echoRunner)
	_, err := d.ResolveCall("connect", map[string]expr.Value{
		"0":    expr.NewString("api"),
		"host": expr.NewString("other"),
	})
	if err == nil || !strings.Contains(err.Error(), "both positionally and by name") {
		t.Errorf("expected dual-bind error, got %v", err)
	}
}

func TestDispatcher_UnknownFunction(t *testing.T) {
	d := funcs.New(map[string]*ir.Function{
		"alpha": &ir.Function{Name: "alpha"},
		"beta":  &ir.Function{Name: "beta"},
	}, echoRunner)
	_, err := d.ResolveCall("missing", nil)
	if err == nil {
		t.Fatal("expected unknown-function error")
	}
	if !strings.Contains(err.Error(), "unknown function") {
		t.Errorf("wrong msg: %v", err)
	}
	// Error should suggest known names.
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("error should list known functions: %v", err)
	}
}

func TestDispatcher_RecursionGuard(t *testing.T) {
	// Build a function whose runner recursively calls itself.
	var d *funcs.Dispatcher
	fn := &ir.Function{Name: "rec", Run: "echo"}
	recurse := func(fn *ir.Function, args map[string]expr.Value) (expr.Value, error) {
		return d.ResolveCall("rec", nil)
	}
	d = funcs.New(map[string]*ir.Function{"rec": fn}, recurse).WithMaxDepth(5)
	_, err := d.ResolveCall("rec", nil)
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Errorf("expected depth-exceeded error, got %v", err)
	}
}

func TestDispatcher_AsExprResolver(t *testing.T) {
	// The dispatcher implements expr.CallResolver, so an expression
	// containing ${call:f(...)} should evaluate end-to-end.
	fn := &ir.Function{
		Name: "double",
		Params: []ir.FunctionParam{
			{Name: "x", Type: "int"},
		},
		Run: "echo",
	}
	doubler := func(fn *ir.Function, args map[string]expr.Value) (expr.Value, error) {
		x, _ := args["x"].AsInt()
		return expr.NewInt(x * 2), nil
	}
	d := funcs.New(map[string]*ir.Function{"double": fn}, doubler)

	node, err := expr.ParseTemplate("${call:double(21)}", expr.Position{})
	if err != nil {
		t.Fatal(err)
	}
	ev := &expr.Evaluator{Funcs: expr.DefaultFuncs(), Calls: d}
	v, err := ev.Eval(node)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if v.Int != 42 {
		t.Errorf("got %d, want 42", v.Int)
	}
}
