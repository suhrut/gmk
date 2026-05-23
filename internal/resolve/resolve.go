// Package resolve provides variable lookup and ${...} substitution for
// gmk var references inside strings.
//
// Stage 3a rewrites the substitution engine: where Stage 2 walked the
// string byte-by-byte and resolved bare ${NAME} refs, Stage 3a uses the
// internal/expr package to parse and evaluate a full expression grammar
// (typed refs, modifiers, pipelines, functions, comparisons). The exported
// function signatures are unchanged — all Stage 1/Stage 2 callers continue
// to work without modification.
//
// Public API:
//
//   - Resolve(name, project)            : Stage 1 entry point
//   - ResolveString(s, project)         : Stage 1 entry point
//   - ResolveInScope(name, scope)       : Stage 2 scope-aware
//   - ResolveStringInScope(s, scope)    : Stage 2 scope-aware
//
// Internally, all four converge on expr.Evaluator + a scopeVarResolver
// adapter that bridges ir.Scope to expr.VarResolver. Cycle detection
// lives in the adapter (using a visit stack) since cycles can span
// nested var refs.
package resolve

import (
	"errors"
	"fmt"
	"os"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/scope"
)

// ErrUndefined indicates a referenced var was not found in scope. Wraps
// expr.ErrUndefinedVar so both old (errors.Is(err, ErrUndefined)) and new
// (errors.Is(err, expr.ErrUndefinedVar)) call sites continue to work.
var ErrUndefined = expr.ErrUndefinedVar

// Resolve returns the resolved string value of a named var in the project's
// root scope. Stage 1 backwards-compat wrapper around ResolveInScope.
func Resolve(name string, p *ir.Project) (string, error) {
	if p == nil || p.RootScope == nil {
		return "", fmt.Errorf("%w: %s (project has no root scope)", ErrUndefined, name)
	}
	return ResolveInScope(name, p.RootScope)
}

// ResolveString substitutes all ${...} references in s against the project's
// root scope. Stage 1 backwards-compat wrapper around ResolveStringInScope.
func ResolveString(s string, p *ir.Project) (string, error) {
	if p == nil || p.RootScope == nil {
		return "", fmt.Errorf("ResolveString: project has no root scope")
	}
	return ResolveStringInScope(s, p.RootScope)
}

// ResolveInScope returns the resolved string value of a named var found by
// walking from the given scope outward (see scope.Lookup for the walk order).
//
// Returns ErrUndefined wrapped with the var name if not found anywhere in
// the scope chain or its includes.
func ResolveInScope(name string, sc *ir.Scope) (string, error) {
	if sc == nil {
		return "", fmt.Errorf("%w: %s (nil scope)", ErrUndefined, name)
	}
	resolver := newScopeResolver(sc)
	val, err := resolver.ResolveVar(name)
	if err != nil {
		return "", err
	}
	return val.AsString(), nil
}

// ResolveStringInScope substitutes all ${...} references in s with their
// resolved values from the given scope.
//
// Stage 3a grammar (full expression language) — see internal/expr for
// details. Notable forms:
//
//	${NAME}                 bare var reference
//	${env:NAME}             OS environment lookup
//	${ctx:NAME}             --ctx flag lookup (S4)
//	${var:NAME}             explicit form of ${NAME}
//	${NAME:-default}        bash-style default
//	${NAME:?required-msg}   bash-style required; errors if unset/empty
//	${NAME:+alternate}      bash-style alternate
//	${X == Y}               equality test
//	${upper(X)}             function call
//	${X | upper | trim}     pipeline
//	$$                      literal dollar sign
//
// Resolution recurses through scope.Lookup: if a var's value contains a
// ${other} ref, that ref is resolved against the same scope, with cycle
// detection.
//
// This entry point does not enable ${render:name(args)} — any render
// reference will fail with "no render resolver configured". Callers
// that need rendering pass a configured RenderResolver via
// ResolveStringInScopeWithRender.
func ResolveStringInScope(s string, sc *ir.Scope) (string, error) {
	return ResolveStringInScopeWithRender(s, sc, nil)
}

// ResolveStringInScopeWithRender is the rendering-aware variant of
// ResolveStringInScope. Used by the CLI's run/dryrun paths where target
// bodies may contain ${render:name(args)} that should expand at body-
// interpolation time (before the shell ever sees the script).
//
// Passing nil for renders is equivalent to ResolveStringInScope and
// makes ${render:...} a runtime error.
func ResolveStringInScopeWithRender(s string, sc *ir.Scope, renders expr.RenderResolver) (string, error) {
	if sc == nil {
		return "", fmt.Errorf("ResolveStringInScopeWithRender: nil scope")
	}
	node, err := expr.ParseTemplate(s, expr.Position{})
	if err != nil {
		return "", err
	}
	resolver := newScopeResolver(sc)
	e := newEvaluator(resolver)
	e.Renders = renders
	return e.EvalToString(node)
}

// ---------------------------------------------------------------------------
// Internal: scope <-> expr.VarResolver adapter with cycle detection.
// ---------------------------------------------------------------------------

// scopeVarResolver bridges ir.Scope (the gmk lexical region) to
// expr.VarResolver (what the expression evaluator needs).
//
// stack tracks the active resolution chain for cycle detection. The
// resolver is single-use per top-level resolve call but is shared across
// the recursive var refs that resolution triggers, so cycle state
// propagates correctly.
type scopeVarResolver struct {
	scope *ir.Scope
	stack []string
}

// newScopeResolver constructs a fresh resolver for one top-level resolve.
func newScopeResolver(sc *ir.Scope) *scopeVarResolver {
	return &scopeVarResolver{scope: sc}
}

// ResolveVar implements expr.VarResolver. Looks up `name` in the resolver's
// scope, evaluates its expression (if any) against the same scope, and
// returns the resulting Value.
//
// Three production paths:
//
//  1. v.Expr != nil           — pre-parsed AST (the load.buildVar path).
//     Most production calls land here.
//  2. v.Expr == nil, no $     — pure literal. Return v.Value directly.
//  3. v.Expr == nil, has $    — fallback: parse v.Value as a template
//     on-the-fly. This supports Vars constructed programmatically
//     (test helpers, future plugin API) without forcing every caller
//     through load.buildVar.
//
// On cycle: returns an error wrapping expr.ErrCycle.
// On miss: returns an error wrapping expr.ErrUndefinedVar (== ErrUndefined).
func (r *scopeVarResolver) ResolveVar(name string) (expr.Value, error) {
	for _, prior := range r.stack {
		if prior == name {
			chain := append([]string{}, r.stack...)
			chain = append(chain, name)
			return expr.NewNone(), fmt.Errorf("%w: %s",
				expr.ErrCycle, joinChain(chain))
		}
	}

	v, _ := scope.Lookup(r.scope, name)
	if v == nil {
		return expr.NewNone(), fmt.Errorf("%w: %s", ErrUndefined, name)
	}

	// Path 1: pre-parsed AST.
	if v.Expr != nil {
		r.stack = append(r.stack, name)
		defer func() { r.stack = r.stack[:len(r.stack)-1] }()
		e := newEvaluator(r)
		return e.Eval(v.Expr)
	}

	// Path 2: pure literal (no template markers). Fast path; the common
	// case for both VarLiteral-tagged production vars and test-helper Vars
	// holding plain strings.
	if !containsTemplateMarker(v.Value) {
		return expr.NewString(v.Value), nil
	}

	// Path 3: Value contains ${...} but wasn't pre-parsed. Parse on the fly.
	// Slower than path 1 but happens once per name per top-level resolve,
	// not in any tight inner loop.
	pos := expr.Position{File: v.Source.File, Line: v.Source.Line, Col: v.Source.Column}
	node, perr := expr.ParseTemplate(v.Value, pos)
	if perr != nil {
		return expr.NewNone(), perr
	}
	r.stack = append(r.stack, name)
	defer func() { r.stack = r.stack[:len(r.stack)-1] }()
	e := newEvaluator(r)
	return e.Eval(node)
}

// containsTemplateMarker is a quick check for "this value might contain
// a substitution". It's intentionally a byte scan rather than a full parse —
// if there's no '$' anywhere, the Value is definitely a pure literal.
func containsTemplateMarker(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '$' {
			return true
		}
	}
	return false
}

// newEvaluator constructs an expr.Evaluator wired up with the standard
// gmk providers (OS env, no-op ctx for Stage 3a) and the default function
// registry.
//
// Stage 4 will swap CtxProvider with a real --ctx flag lookup.
// Stage 7 will add a ProbeProvider.
// Stage 12 will allow plugins to register additional Funcs.
func newEvaluator(vars expr.VarResolver) *expr.Evaluator {
	return &expr.Evaluator{
		Vars:        vars,
		EnvProvider: os.LookupEnv,
		CtxProvider: stubCtxProvider, // S3a: never set; modifiers detect via IsEmpty
		Funcs:       expr.DefaultFuncs(),
	}
}

// stubCtxProvider returns (_, false) for everything. The --ctx flag isn't
// wired up until Stage 4; for now, ${ctx:NAME} resolves to None and
// modifiers can supply defaults or required-errors.
func stubCtxProvider(name string) (string, bool) { return "", false }

// joinChain renders a stack of var names with " -> " separators.
func joinChain(chain []string) string {
	if len(chain) == 0 {
		return ""
	}
	out := chain[0]
	for _, c := range chain[1:] {
		out += " -> " + c
	}
	return out
}

// Compile-time check: the package's ErrUndefined wraps expr.ErrUndefinedVar.
// Callers using errors.Is(err, resolve.ErrUndefined) continue to match
// expression-layer undefined-var errors transparently.
var _ = errors.Is
