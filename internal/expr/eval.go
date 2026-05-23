package expr

import (
	"errors"
)

// CallResolver dispatches `call:` NamedCalls to user-defined functions
// or plugin entry points. Implemented outside the expr package (by
// internal/resolve or internal/funcs) so the expression layer stays
// free of project-shape knowledge.
//
// ResolveCall is invoked with the function name and a map of evaluated
// argument values (already reduced to Values by the Evaluator). It
// returns the function's result Value, or an error. The map may
// include both named and positional args: positional args use the
// special keys "0", "1", "2", ... matching their declaration order.
type CallResolver interface {
	ResolveCall(name string, args map[string]Value) (Value, error)
}

// RenderResolver dispatches `render:` NamedCalls to the project's
// template machinery. Like CallResolver, implemented outside expr so
// the expression layer stays clean of project-shape knowledge.
//
// ResolveRender is invoked with the template name (already reduced
// from any expression-form name like dockerfile-${lang}) and a map of
// evaluated argument values. The returned Value is always a String
// (the rendered output); engines that fail produce an error.
//
// Stage 3c: ResolveRender exists to satisfy the same separation-of-
// concerns wall that CallResolver does. The implementation lives in
// internal/resolve and bridges to internal/template via the project's
// ir.Template map.
type RenderResolver interface {
	ResolveRender(name string, args map[string]Value) (Value, error)
}

// VarResolver is what an Evaluator needs to look up bare ${name} refs.
//
// ResolveVar should:
//   - Look up name in scope
//   - Evaluate the var's expression body recursively
//   - Detect cycles (a -> ${b} -> ${a})
//   - Return ErrUndefinedVar wrapped if not found
type VarResolver interface {
	ResolveVar(name string) (Value, error)
}

// ProviderFunc is the function type for env/ctx lookups. Returning (_, false)
// indicates "not set"; the evaluator treats this as a None value, which
// modifiers can detect via IsEmpty.
type ProviderFunc func(name string) (string, bool)

// Evaluator reduces AST nodes to Values against a scope (via VarResolver)
// and external providers (env, ctx). Function dispatch is via Funcs;
// `call:` NamedCalls dispatch via Calls (may be nil — in that case
// NamedCalls fail with a clear error). `render:` NamedCalls dispatch
// via Renders (may be nil — same fail-clear behavior).
//
// An Evaluator is single-use per resolution (the underlying VarResolver
// implementation tracks its own cycle state). Reusing across unrelated
// resolutions is safe but doesn't compose cycle detection across them.
type Evaluator struct {
	Vars        VarResolver
	EnvProvider ProviderFunc
	CtxProvider ProviderFunc
	Funcs       *FuncRegistry  // nil -> uses DefaultFuncs()
	Calls       CallResolver   // Stage 3b: nil disables ${call:...}
	Renders     RenderResolver // Stage 3c: nil disables ${render:...}
}

// Eval reduces a node to a Value.
func (e *Evaluator) Eval(n Node) (Value, error) {
	if n == nil {
		return NewNone(), nil
	}
	switch x := n.(type) {
	case *Literal:
		return x.Value, nil

	case *VarRef:
		if e.Vars == nil {
			return NewNone(), NewEvalError(x.P, ErrUndefinedVar, "%s", x.Name)
		}
		v, err := e.Vars.ResolveVar(x.Name)
		if err != nil {
			// Preserve sentinel wrapping; add position context.
			return NewNone(), NewEvalError(x.P, err, "${%s}", x.Name)
		}
		return v, nil

	case *TypedRef:
		return e.evalTypedRef(x)

	case *FuncCall:
		return e.evalFuncCall(x, nil)

	case *Pipeline:
		return e.evalPipeline(x)

	case *Modifier:
		return e.evalModifier(x)

	case *BinaryOp:
		return e.evalBinaryOp(x)

	case *Concat:
		return e.evalConcat(x)

	case *FieldAccess:
		// Stage 3b: ${inner.field}
		inner, err := e.Eval(x.Inner)
		if err != nil {
			return NewNone(), err
		}
		v, err := inner.Field(x.Field)
		if err != nil {
			return NewNone(), NewEvalError(x.P, err, "field access .%s", x.Field)
		}
		return v, nil

	case *IndexAccess:
		// Stage 3b: ${inner[key]}
		inner, err := e.Eval(x.Inner)
		if err != nil {
			return NewNone(), err
		}
		key, err := e.Eval(x.Key)
		if err != nil {
			return NewNone(), err
		}
		v, err := inner.Index(key)
		if err != nil {
			return NewNone(), NewEvalError(x.P, err, "index access")
		}
		return v, nil

	case *NamedCall:
		// Stage 3b: ${call:fn(args)}.
		// The "call" kind dispatches via the optional CallResolver
		// interface; other kinds aren't recognized yet. Builtin funcs
		// (upper, default, ...) still arrive as FuncCall and stay on
		// their own path; NamedCall is for user-defined and plugin
		// callables that take named arguments.
		return e.evalNamedCall(x)
	}
	return NewNone(), NewEvalError(n.Pos(), nil, "unsupported node type %T", n)
}

// EvalToString is a convenience wrapper: Eval + AsString. Used by
// resolve.ResolveString.
func (e *Evaluator) EvalToString(n Node) (string, error) {
	v, err := e.Eval(n)
	if err != nil {
		return "", err
	}
	return v.AsString(), nil
}

// evalTypedRef looks up env/ctx/var-prefixed refs.
func (e *Evaluator) evalTypedRef(t *TypedRef) (Value, error) {
	switch t.Kind {
	case "env":
		if e.EnvProvider == nil {
			return NewNone(), nil
		}
		s, ok := e.EnvProvider(t.Path)
		if !ok {
			return NewNone(), nil // unset env -> None; modifiers can defaulting
		}
		return NewString(s), nil

	case "ctx":
		if e.CtxProvider == nil {
			return NewNone(), nil
		}
		s, ok := e.CtxProvider(t.Path)
		if !ok {
			return NewNone(), nil
		}
		return NewString(s), nil

	case "var":
		// Explicit form: ${var:name} == ${name}. Useful in YAML libraries
		// to disambiguate from typed refs that might share names.
		if e.Vars == nil {
			return NewNone(), NewEvalError(t.P, ErrUndefinedVar, "%s", t.Path)
		}
		v, err := e.Vars.ResolveVar(t.Path)
		if err != nil {
			return NewNone(), NewEvalError(t.P, err, "${var:%s}", t.Path)
		}
		return v, nil

	default:
		// probe (S7), plugin-defined (S12), etc.
		return NewNone(), NewEvalError(t.P, nil,
			"unknown typed-ref kind %q (Stage 3a supports: env, ctx, var)", t.Kind)
	}
}

// evalFuncCall invokes a registered function. extraArgs is prepended to the
// declared args (used by pipelines to insert the piped value).
func (e *Evaluator) evalFuncCall(f *FuncCall, extraArgs []Value) (Value, error) {
	registry := e.Funcs
	if registry == nil {
		registry = DefaultFuncs()
	}
	fn, ok := registry.Get(f.Name)
	if !ok {
		return NewNone(), NewEvalError(f.P, ErrUndefinedFunc, "%s", f.Name)
	}
	args := make([]Value, 0, len(extraArgs)+len(f.Args))
	args = append(args, extraArgs...)
	for _, a := range f.Args {
		v, err := e.Eval(a)
		if err != nil {
			return NewNone(), err
		}
		args = append(args, v)
	}
	out, err := fn(args)
	if err != nil {
		return NewNone(), NewEvalError(f.P, err, "%s()", f.Name)
	}
	return out, nil
}

// evalPipeline evaluates source and threads its result through each stage.
func (e *Evaluator) evalPipeline(p *Pipeline) (Value, error) {
	val, err := e.Eval(p.Source)
	if err != nil {
		return NewNone(), err
	}
	for _, stage := range p.Stages {
		val, err = e.evalFuncCall(stage, []Value{val})
		if err != nil {
			return NewNone(), err
		}
	}
	return val, nil
}

// evalModifier applies bash-compatible default/required/alternate logic.
func (e *Evaluator) evalModifier(m *Modifier) (Value, error) {
	inner, err := e.Eval(m.Inner)
	// For ModRequired we want the unmodified error (it carries useful
	// context like "undefined var X"). For ModDefault/ModAlternate, an
	// "undefined" error short-circuits to the modifier behaviour because
	// undefined-var is also semantically "not set".
	switch m.Kind {
	case ModDefault:
		if err != nil && errors.Is(err, ErrUndefinedVar) {
			return NewString(m.Arg), nil
		}
		if err != nil {
			return NewNone(), err
		}
		if inner.IsEmpty() {
			return NewString(m.Arg), nil
		}
		return inner, nil

	case ModRequired:
		if err != nil && errors.Is(err, ErrUndefinedVar) {
			return NewNone(), NewEvalError(m.P, ErrRequired, "%s", m.Arg)
		}
		if err != nil {
			return NewNone(), err
		}
		if inner.IsEmpty() {
			return NewNone(), NewEvalError(m.P, ErrRequired, "%s", m.Arg)
		}
		return inner, nil

	case ModAlternate:
		if err != nil && errors.Is(err, ErrUndefinedVar) {
			return NewString(""), nil
		}
		if err != nil {
			return NewNone(), err
		}
		if inner.IsEmpty() {
			return NewString(""), nil
		}
		return NewString(m.Arg), nil
	}
	return NewNone(), NewEvalError(m.P, nil, "unsupported modifier %v", m.Kind)
}

// evalBinaryOp evaluates ==, !=. Stage 4 will extend this for &&, ||, <, etc.
func (e *Evaluator) evalBinaryOp(b *BinaryOp) (Value, error) {
	lhs, err := e.Eval(b.Lhs)
	if err != nil {
		return NewNone(), err
	}
	rhs, err := e.Eval(b.Rhs)
	if err != nil {
		return NewNone(), err
	}
	switch b.Op {
	case "==":
		return NewBool(lhs.Equal(rhs)), nil
	case "!=":
		return NewBool(!lhs.Equal(rhs)), nil
	}
	return NewNone(), NewEvalError(b.P, nil, "unknown operator %q", b.Op)
}

// evalConcat evaluates all parts and joins their string forms.
func (e *Evaluator) evalConcat(c *Concat) (Value, error) {
	var out string
	for _, p := range c.Parts {
		v, err := e.Eval(p)
		if err != nil {
			return NewNone(), err
		}
		out += v.AsString()
	}
	return NewString(out), nil
}

// evalNamedCall dispatches a NamedCall to the appropriate resolver
// based on Kind. Arguments are evaluated left-to-right; positional
// args go into the map under stringified indices ("0", "1", ...),
// named args under their names.
//
// Stage 3c: two kinds are recognised — "call" (Stage 3b, dispatches to
// CallResolver) and "render" (Stage 3c, dispatches to RenderResolver).
// Future kinds (e.g. "secret:") fail predictably with a clear error
// until they're implemented.
//
// Why a map and not an ordered list: receivers care about names; the
// few callers that need positional ordering can read "0", "1", "2".
// This avoids inventing a tagged-arg list type, and the JSON
// boundary contract for the eventual gmk call CLI uses a map too.
func (e *Evaluator) evalNamedCall(n *NamedCall) (Value, error) {
	// Argument evaluation is identical across kinds, so do it once
	// up-front before dispatching.
	//
	// Stage 3c.1.1: splat args are evaluated, asserted to be a Map,
	// and their entries spread into the args map. Splat-then-named
	// arg ordering means later-named-args win on key conflict, which
	// is the principle-of-least-surprise rule the user expects.
	args := make(map[string]Value, len(n.Args))
	posIdx := 0
	for _, a := range n.Args {
		v, err := e.Eval(a.Value)
		if err != nil {
			return NewNone(), err
		}
		switch {
		case a.IsSplat:
			if v.Kind != MapKind {
				return NewNone(), NewEvalError(n.P, nil,
					"splat argument must be a map, got %s", v.Kind.String())
			}
			for k, item := range v.Map {
				args[k] = item
			}
		case a.Name == "":
			args[itoa(posIdx)] = v
			posIdx++
		default:
			args[a.Name] = v
		}
	}

	switch n.Kind {
	case "call":
		if e.Calls == nil {
			return NewNone(), NewEvalError(n.P, nil,
				"no call resolver configured; cannot evaluate ${call:%s(...)}", n.Name)
		}
		out, err := e.Calls.ResolveCall(n.Name, args)
		if err != nil {
			return NewNone(), NewEvalError(n.P, err, "call:%s", n.Name)
		}
		return out, nil

	case "render":
		if e.Renders == nil {
			return NewNone(), NewEvalError(n.P, nil,
				"no render resolver configured; cannot evaluate ${render:%s(...)}", n.Name)
		}
		out, err := e.Renders.ResolveRender(n.Name, args)
		if err != nil {
			return NewNone(), NewEvalError(n.P, err, "render:%s", n.Name)
		}
		return out, nil

	case "map", "filter":
		// Stage 3c.2: iteration combinators. Both share the same arg
		// shape — `items=L` is the iterable, every other arg is
		// "pinned" and passed through unchanged on each element. The
		// element binds to the named arg `item` per iteration.
		//
		// map:    ${map:fn(items=L, pinA=...)}    → call fn(item=X, pinA=...) per X; return List of results
		// filter: ${filter:fn(items=L, pinA=...)} → call fn(item=X, pinA=...) per X; keep X when fn returned true
		//
		// The choice to make the iteration variable name fixed (`item`)
		// rather than configurable is a deliberate restriction kept
		// small for the initial implementation. A future stage can
		// add `as=name` once a real use case demands it without breaking
		// any code that uses the fixed-name convention.
		if e.Calls == nil {
			return NewNone(), NewEvalError(n.P, nil,
				"no call resolver configured; cannot evaluate ${%s:%s(...)}", n.Kind, n.Name)
		}
		itemsVal, ok := args["items"]
		if !ok {
			return NewNone(), NewEvalError(n.P, nil,
				"%s:%s: required arg `items` is missing", n.Kind, n.Name)
		}
		if itemsVal.Kind != ListKind {
			return NewNone(), NewEvalError(n.P, nil,
				"%s:%s: `items` must be a list, got %s", n.Kind, n.Name, itemsVal.Kind.String())
		}
		// Build the pinned-args map once. We'll add `item=X` per
		// iteration. Shallow-copying is fine — Value is a value-type;
		// nested maps/lists share underlying slices but we don't
		// mutate them.
		pinned := make(map[string]Value, len(args))
		for k, v := range args {
			if k == "items" {
				continue
			}
			pinned[k] = v
		}
		// Guard the user against shadowing the iteration variable
		// with their own `item=...` — that's almost certainly a bug.
		if _, has := pinned["item"]; has {
			return NewNone(), NewEvalError(n.P, nil,
				"%s:%s: arg `item` conflicts with the per-iteration binding name (rename your pinned arg)", n.Kind, n.Name)
		}
		results := make([]Value, 0, len(itemsVal.List))
		for idx, elem := range itemsVal.List {
			callArgs := make(map[string]Value, len(pinned)+1)
			for k, v := range pinned {
				callArgs[k] = v
			}
			callArgs["item"] = elem
			out, err := e.Calls.ResolveCall(n.Name, callArgs)
			if err != nil {
				return NewNone(), NewEvalError(n.P, err,
					"%s:%s[%d]", n.Kind, n.Name, idx)
			}
			if n.Kind == "map" {
				results = append(results, out)
				continue
			}
			// filter: predicate must return a bool.
			if out.Kind != BoolKind {
				return NewNone(), NewEvalError(n.P, nil,
					"filter:%s[%d]: predicate returned %s, expected bool",
					n.Name, idx, out.Kind.String())
			}
			if out.Bool {
				results = append(results, elem)
			}
		}
		return NewList(results), nil

	default:
		return NewNone(), NewEvalError(n.P, nil,
			"unsupported call kind %q (recognised: call, render, map, filter)", n.Kind)
	}
}

// itoa is a tiny strconv-free int-to-string helper for the positional
// arg keys in evalNamedCall. Avoids pulling in strconv just for this.
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
