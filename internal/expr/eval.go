package expr

import (
	"errors"
)

// VarResolver is what an Evaluator needs to look up bare ${name} refs.
// Implemented by package resolve (which adapts ir.Scope to this surface).
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
// and external providers (env, ctx). Function dispatch is via Funcs.
//
// An Evaluator is single-use per resolution (the underlying VarResolver
// implementation tracks its own cycle state). Reusing across unrelated
// resolutions is safe but doesn't compose cycle detection across them.
type Evaluator struct {
	Vars        VarResolver
	EnvProvider ProviderFunc
	CtxProvider ProviderFunc
	Funcs       *FuncRegistry // nil -> uses DefaultFuncs()
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
