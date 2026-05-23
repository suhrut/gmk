// Package funcs implements the runtime call dispatcher for Stage 3b
// user-defined functions, plus the (stubbed) plugin lookup path.
//
// Architecture:
//
//   - expr.NamedCall nodes are evaluated against the expr.CallResolver
//     interface. This package provides Dispatcher, the concrete
//     implementation that turns a `${call:name(...)}` evaluation request
//     into a function execution.
//
//   - For Stage 3b, function execution is "inline by reference":
//     the dispatcher receives the function's evaluated arg map and
//     hands off to a Runner closure that the project layer provides.
//     The Runner is responsible for evaluating the function's prelude,
//     writing JSON files to the scratch dir, invoking the body, and
//     reading $GMK_RESULT back. This package stays free of materialize
//     and runner dependencies.
//
//   - Plugin lookup is stubbed: any call:name not found in the local
//     project returns an error mentioning plugins as the not-yet path.
package funcs

import (
	"fmt"
	"sync"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/logger"
)

// Runner is what the Dispatcher calls to actually execute a function.
// Returns the function's result Value (whatever the body wrote to
// $GMK_RESULT, parsed via expr.FromJSON), or an error.
//
// The Runner closure is supplied by the project layer (cli/run.go);
// it has access to the materialize/runner subsystems that this package
// intentionally does not depend on.
type Runner func(fn *ir.Function, args map[string]expr.Value) (expr.Value, error)

// Dispatcher implements expr.CallResolver against a project's function
// table. It also caches results for pure functions (Stage 3b: no caching
// — every call re-runs; cache layer arrives in Stage 6).
//
// The dispatcher is safe for concurrent use across goroutines, anticipating
// the parallel-execution work in Stage 5.
type Dispatcher struct {
	functions map[string]*ir.Function
	runner    Runner

	mu       sync.Mutex
	depthCtr int    // current call depth for cycle/recursion detection
	maxDepth int    // hard ceiling; 0 = use default
	chain    []string
}

// New returns a Dispatcher backed by the given function table and runner.
// Either or both may be nil; nil functions table means no calls succeed,
// nil runner means dispatcher panics at the runner call site (caller's
// bug, not user error).
func New(functions map[string]*ir.Function, runner Runner) *Dispatcher {
	return &Dispatcher{
		functions: functions,
		runner:    runner,
		maxDepth:  64, // arbitrary but high enough for legitimate recursion
	}
}

// WithMaxDepth returns a new Dispatcher with the same functions and runner
// but a different recursion ceiling. Used in tests and in environments
// that want to be more permissive (or more restrictive).
func (d *Dispatcher) WithMaxDepth(n int) *Dispatcher {
	return &Dispatcher{
		functions: d.functions,
		runner:    d.runner,
		maxDepth:  n,
	}
}

// ResolveCall is the expr.CallResolver interface. The args map uses
// "0", "1", ... for positional args and parameter names for keyword args
// — matching what expr.evalNamedCall produces.
//
// Resolution steps:
//
//	1. Look up name in the local functions table.
//	2. If found, bind args to params (apply defaults, type-check),
//	   call the Runner.
//	3. If not found, return an error mentioning that plugin lookup
//	   hasn't landed yet (Stage 3f).
//
// Recursion guard: a per-Dispatcher counter prevents runaway recursion
// in case a function calls itself directly or via a cycle. The chain
// list is included in the error so users can diagnose the cycle.
func (d *Dispatcher) ResolveCall(name string, args map[string]expr.Value) (expr.Value, error) {
	d.mu.Lock()
	if d.depthCtr >= d.maxDepth {
		chain := append([]string{}, d.chain...)
		chain = append(chain, name)
		d.mu.Unlock()
		return expr.NewNone(), fmt.Errorf("call depth %d exceeded (chain: %v)", d.maxDepth, chain)
	}
	d.depthCtr++
	d.chain = append(d.chain, name)
	chainSnapshot := append([]string{}, d.chain...)
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.depthCtr--
		if len(d.chain) > 0 {
			d.chain = d.chain[:len(d.chain)-1]
		}
		d.mu.Unlock()
	}()

	logger.Get("gmk.funcs.dispatch").Debug("call",
		"callable", name, "args_count", len(args), "depth", len(chainSnapshot))

	fn, ok := d.functions[name]
	if !ok {
		return expr.NewNone(), fmt.Errorf("unknown function %q "+
			"(local functions: %v; plugin lookup not implemented yet)",
			name, knownFunctionNames(d.functions))
	}

	bound, err := bindArgs(fn, args)
	if err != nil {
		return expr.NewNone(), fmt.Errorf("call:%s: %w", name, err)
	}

	if d.runner == nil {
		return expr.NewNone(), fmt.Errorf("call:%s: no runner configured", name)
	}
	result, err := d.runner(fn, bound)
	if err != nil {
		return expr.NewNone(), fmt.Errorf("call:%s: %w", name, err)
	}
	return result, nil
}

// bindArgs binds caller-supplied args to a function's declared params:
//
//   - Positional args ("0", "1", ...) fill in declared params left to right.
//   - Named args fill in by name.
//   - Missing params take their default if any.
//   - Missing required params (no default) cause an error.
//   - Extra positional args cause an error.
//   - Extra named args cause an error.
//   - Type coercion happens here: callers may pass a string "42" for an
//     int param and we'll coerce; passing "hello" for an int errors out.
//
// The returned map is keyed by param name only (no positional indices)
// — that's the shape the Runner expects.
func bindArgs(fn *ir.Function, args map[string]expr.Value) (map[string]expr.Value, error) {
	out := make(map[string]expr.Value, len(fn.Params))
	consumed := make(map[string]bool, len(args))

	// First, walk declared params in order, pulling positional args
	// (by index string) or named args (by name) as appropriate.
	for i, p := range fn.Params {
		posKey := itoa(i)
		var v expr.Value
		var have bool
		if pv, ok := args[posKey]; ok {
			// Caller passed it positionally.
			v = pv
			consumed[posKey] = true
			have = true
		}
		if nv, ok := args[p.Name]; ok {
			if have {
				return nil, fmt.Errorf("param %q passed both positionally and by name", p.Name)
			}
			v = nv
			consumed[p.Name] = true
			have = true
		}
		if !have {
			if p.Default == nil {
				return nil, fmt.Errorf("required param %q not provided", p.Name)
			}
			// Evaluate the default expression. Defaults are evaluated
			// each call (so they can reference run-time values like env);
			// in this Stage 3b cut we evaluate with no scope, which
			// means defaults must be self-contained — literals or
			// pure-builtin pipelines. Fuller scoping arrives in S4.
			ev := &expr.Evaluator{Funcs: expr.DefaultFuncs()}
			dv, derr := ev.Eval(*p.Default)
			if derr != nil {
				return nil, fmt.Errorf("evaluating default for %q: %w", p.Name, derr)
			}
			v = dv
		}
		// Type coerce. If coercion fails, return an error mentioning
		// the param name so the user knows where to look.
		coerced, cerr := coerceValueToType(v, p.Type)
		if cerr != nil {
			return nil, fmt.Errorf("param %q: %w", p.Name, cerr)
		}
		out[p.Name] = coerced
	}

	// Any leftover args are unexpected — either extra positionals beyond
	// the declared params, or named args with no matching param.
	for k := range args {
		if !consumed[k] {
			return nil, fmt.Errorf("unexpected argument %q (declared params: %v)",
				k, paramNames(fn.Params))
		}
	}
	return out, nil
}

// coerceValueToType returns v converted to the named type, or an error.
// The accepted types match isValidParamType in the loader.
//
// Coercion is "permissive but not lossy": strings that parse as ints
// become ints; structured values must arrive as the right kind already.
func coerceValueToType(v expr.Value, t string) (expr.Value, error) {
	switch t {
	case "string":
		return expr.NewString(v.AsString()), nil
	case "int":
		n, err := v.AsInt()
		if err != nil {
			return expr.NewNone(), fmt.Errorf("cannot coerce %s to int: %w", v.Kind, err)
		}
		return expr.NewInt(n), nil
	case "float":
		// Convert via AsString -> parse, since Value has no direct
		// AsFloat. Coerce ints exactly.
		switch v.Kind {
		case expr.FloatKind:
			return v, nil
		case expr.IntKind:
			return expr.NewFloat(float64(v.Int)), nil
		case expr.StringKind:
			f, err := parseFloat(v.Str)
			if err != nil {
				return expr.NewNone(), fmt.Errorf("cannot parse %q as float", v.Str)
			}
			return expr.NewFloat(f), nil
		}
		return expr.NewNone(), fmt.Errorf("cannot coerce %s to float", v.Kind)
	case "bool":
		return expr.NewBool(v.AsBool()), nil
	case "list":
		if v.Kind != expr.ListKind {
			return expr.NewNone(), fmt.Errorf("expected list, got %s", v.Kind)
		}
		return v, nil
	case "map":
		if v.Kind != expr.MapKind {
			return expr.NewNone(), fmt.Errorf("expected map, got %s", v.Kind)
		}
		return v, nil
	}
	return expr.NewNone(), fmt.Errorf("unknown type %q", t)
}

// knownFunctionNames returns the names of declared functions, sorted.
// Used in error messages to help the user spot typos.
func knownFunctionNames(fns map[string]*ir.Function) []string {
	out := make([]string, 0, len(fns))
	for name := range fns {
		out = append(out, name)
	}
	// Bubble sort — tiny inputs, no strconv-style imports needed.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func paramNames(ps []ir.FunctionParam) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// itoa is a tiny helper duplicated locally to keep this package free
// of strconv (small dep hygiene), matching the convention in expr/eval.go.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// parseFloat is a minimal float parser. We use the stdlib elsewhere
// (json), so pulling in strconv would be fine, but keeping this package
// minimal makes the dep graph visible.
func parseFloat(s string) (float64, error) {
	// Forward to the stdlib via a tiny indirection so we don't grow this
	// package's surface unnecessarily. (Inlining a parser would be silly.)
	return parseFloatStdlib(s)
}
