package expr

import (
	"errors"
	"fmt"
)

// Sentinel errors so callers (e.g. resolve, load) can match with errors.Is.
var (
	// ErrParse indicates a problem at parse time: bad syntax, unterminated
	// expression, unknown operator, etc.
	ErrParse = errors.New("parse error")

	// ErrEval indicates a problem at evaluation time: type coercion failure,
	// unknown function, missing required var, etc.
	ErrEval = errors.New("evaluation error")

	// ErrUndefinedVar indicates a ${name} reference whose name was not in
	// scope. resolve.ErrUndefined wraps this for backwards compatibility
	// with Stage 2 callers.
	ErrUndefinedVar = errors.New("undefined variable")

	// ErrUndefinedFunc indicates a function name not registered.
	ErrUndefinedFunc = errors.New("undefined function")

	// ErrRequired is the error returned when an ${X:?msg} modifier fires
	// because X is empty/unset. The Msg in the wrapped error is the
	// user-supplied :?-argument.
	ErrRequired = errors.New("required variable is unset or empty")

	// ErrCycle is returned when var resolution detects a cycle, e.g.
	// a -> ${b}, b -> ${a}.
	ErrCycle = errors.New("cyclic variable reference")
)

// ParseError carries a Position so error messages can pinpoint a YAML
// file:line:col. Wraps ErrParse so callers can use errors.Is.
type ParseError struct {
	Pos Position
	Msg string
}

func (e *ParseError) Error() string {
	if e.Pos.File == "" && e.Pos.Line == 0 {
		return "parse error: " + e.Msg
	}
	return fmt.Sprintf("parse error at %s: %s", e.Pos, e.Msg)
}

// Unwrap returns ErrParse so errors.Is(err, ErrParse) works.
func (e *ParseError) Unwrap() error { return ErrParse }

// NewParseError constructs a ParseError with the given position and message.
func NewParseError(pos Position, format string, args ...any) *ParseError {
	return &ParseError{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

// EvalError wraps an evaluation-time failure with position and optional
// underlying cause. Wraps a sentinel error so callers can match with
// errors.Is.
type EvalError struct {
	Pos   Position
	Msg   string
	Inner error // optional; preserves the wrapping chain for errors.Is/As
}

func (e *EvalError) Error() string {
	prefix := "eval error"
	if e.Pos.File != "" || e.Pos.Line != 0 {
		prefix = fmt.Sprintf("eval error at %s", e.Pos)
	}
	if e.Msg == "" && e.Inner != nil {
		return prefix + ": " + e.Inner.Error()
	}
	if e.Inner == nil {
		return prefix + ": " + e.Msg
	}
	return prefix + ": " + e.Msg + ": " + e.Inner.Error()
}

// Unwrap returns the inner error (e.g. ErrUndefinedVar) so errors.Is works
// for sentinel matching. If Inner is nil, returns ErrEval.
func (e *EvalError) Unwrap() error {
	if e.Inner != nil {
		return e.Inner
	}
	return ErrEval
}

// NewEvalError constructs an EvalError. Pass nil for inner if there's no
// underlying cause.
func NewEvalError(pos Position, inner error, format string, args ...any) *EvalError {
	return &EvalError{Pos: pos, Inner: inner, Msg: fmt.Sprintf(format, args...)}
}
