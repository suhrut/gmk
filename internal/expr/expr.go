package expr

// expr.go gathers the package's external API surface in one file for
// discoverability. Implementations live in:
//
//   ast.go     - Node types (Literal, VarRef, TypedRef, FuncCall, Pipeline,
//                Modifier, BinaryOp, Concat) and Position
//   errors.go  - ParseError, EvalError, sentinel errors
//   lexer.go   - Token, Lexer
//   parser.go  - ParseTemplate, ParseExpr
//   eval.go    - Evaluator, VarResolver interface, ProviderFunc
//   funcs.go   - Func, FuncRegistry, DefaultFuncs, built-ins
//   value.go   - Value, ValueKind, NewString/NewInt/etc, accessors
//
// Stage 4 will extend with: more BinaryOp operators (&&, ||, <, >, <=, >=, =~),
// UnaryOp (!), comparison chains. The grammar extension lives in parser.go;
// the evaluator extension lives in eval.go. The public API in this file
// stays stable.
//
// Stage 5 will add lazy-eval markers (TagCall as a non-evaluating Node).
// Stage 7 will add probe TypedRef support in evalTypedRef.
// Stage 12 will let plugins register Funcs at runtime.

// CoerceToString is a public convenience for callers that have a Value
// and want its rendered string form. Equivalent to v.AsString() but
// available as a free function for use in defaults / templates.
func CoerceToString(v Value) string { return v.AsString() }

// IsRef reports whether a node is a variable-reference form (VarRef or
// TypedRef). Used by callers that want to detect "this is a lookup, not
// a literal/expression". load uses this to validate where modifiers can
// be attached.
func IsRef(n Node) bool {
	switch n.(type) {
	case *VarRef, *TypedRef:
		return true
	}
	return false
}
