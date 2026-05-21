package expr

import (
	"fmt"
	"strings"
)

// Func is a built-in or plugin-registered function. Receives a slice of
// already-evaluated arguments (pipelines have already prepended the
// piped value). Returns a Value and an optional error.
type Func func(args []Value) (Value, error)

// FuncRegistry maps function names to implementations. Threadsafe for
// concurrent reads after construction; Register is not threadsafe (only
// called during init).
type FuncRegistry struct {
	funcs map[string]Func
}

// NewFuncRegistry returns an empty registry.
func NewFuncRegistry() *FuncRegistry {
	return &FuncRegistry{funcs: make(map[string]Func)}
}

// Register adds a function. Overwrites any prior entry with the same name.
func (r *FuncRegistry) Register(name string, fn Func) {
	r.funcs[name] = fn
}

// Get returns the function and true if registered, else nil and false.
func (r *FuncRegistry) Get(name string) (Func, bool) {
	fn, ok := r.funcs[name]
	return fn, ok
}

// Names returns the registered function names. Used for diagnostics
// and `gmk funcs` (S11).
func (r *FuncRegistry) Names() []string {
	names := make([]string, 0, len(r.funcs))
	for n := range r.funcs {
		names = append(names, n)
	}
	return names
}

// DefaultFuncs returns a registry pre-loaded with Stage 3a built-ins.
// Each call returns a fresh registry (callers can extend it without
// affecting other Evaluators).
func DefaultFuncs() *FuncRegistry {
	r := NewFuncRegistry()
	r.Register("upper", fnUpper)
	r.Register("lower", fnLower)
	r.Register("trim", fnTrim)
	r.Register("trim_left", fnTrimLeft)
	r.Register("trim_right", fnTrimRight)
	r.Register("to_string", fnToString)
	r.Register("to_int", fnToInt)
	r.Register("len", fnLen)
	r.Register("starts_with", fnStartsWith)
	r.Register("ends_with", fnEndsWith)
	r.Register("contains", fnContains)
	r.Register("replace", fnReplace)
	r.Register("default", fnDefault)
	r.Register("coalesce", fnCoalesce)
	r.Register("join", fnJoin)
	return r
}

// ---- helpers ----

func checkArity(name string, args []Value, want int) error {
	if len(args) != want {
		return fmt.Errorf("%s expects %d args, got %d", name, want, len(args))
	}
	return nil
}

func checkArityRange(name string, args []Value, minN, maxN int) error {
	if len(args) < minN || len(args) > maxN {
		return fmt.Errorf("%s expects %d-%d args, got %d", name, minN, maxN, len(args))
	}
	return nil
}

// ---- built-in implementations ----

func fnUpper(args []Value) (Value, error) {
	if err := checkArity("upper", args, 1); err != nil {
		return NewNone(), err
	}
	return NewString(strings.ToUpper(args[0].AsString())), nil
}

func fnLower(args []Value) (Value, error) {
	if err := checkArity("lower", args, 1); err != nil {
		return NewNone(), err
	}
	return NewString(strings.ToLower(args[0].AsString())), nil
}

func fnTrim(args []Value) (Value, error) {
	if err := checkArity("trim", args, 1); err != nil {
		return NewNone(), err
	}
	return NewString(strings.TrimSpace(args[0].AsString())), nil
}

func fnTrimLeft(args []Value) (Value, error) {
	if err := checkArityRange("trim_left", args, 1, 2); err != nil {
		return NewNone(), err
	}
	s := args[0].AsString()
	if len(args) == 2 {
		return NewString(strings.TrimLeft(s, args[1].AsString())), nil
	}
	// Default cutset: whitespace.
	return NewString(strings.TrimLeft(s, " \t\n\r")), nil
}

func fnTrimRight(args []Value) (Value, error) {
	if err := checkArityRange("trim_right", args, 1, 2); err != nil {
		return NewNone(), err
	}
	s := args[0].AsString()
	if len(args) == 2 {
		return NewString(strings.TrimRight(s, args[1].AsString())), nil
	}
	return NewString(strings.TrimRight(s, " \t\n\r")), nil
}

func fnToString(args []Value) (Value, error) {
	if err := checkArity("to_string", args, 1); err != nil {
		return NewNone(), err
	}
	return NewString(args[0].AsString()), nil
}

func fnToInt(args []Value) (Value, error) {
	if err := checkArity("to_int", args, 1); err != nil {
		return NewNone(), err
	}
	n, err := args[0].AsInt()
	if err != nil {
		return NewNone(), err
	}
	return NewInt(n), nil
}

// fnLen returns the length of a string or list.
func fnLen(args []Value) (Value, error) {
	if err := checkArity("len", args, 1); err != nil {
		return NewNone(), err
	}
	switch args[0].Kind {
	case StringKind:
		return NewInt(int64(len(args[0].Str))), nil
	case ListKind:
		return NewInt(int64(len(args[0].List))), nil
	case NoneKind:
		return NewInt(0), nil
	default:
		return NewInt(int64(len(args[0].AsString()))), nil
	}
}

func fnStartsWith(args []Value) (Value, error) {
	if err := checkArity("starts_with", args, 2); err != nil {
		return NewNone(), err
	}
	return NewBool(strings.HasPrefix(args[0].AsString(), args[1].AsString())), nil
}

func fnEndsWith(args []Value) (Value, error) {
	if err := checkArity("ends_with", args, 2); err != nil {
		return NewNone(), err
	}
	return NewBool(strings.HasSuffix(args[0].AsString(), args[1].AsString())), nil
}

func fnContains(args []Value) (Value, error) {
	if err := checkArity("contains", args, 2); err != nil {
		return NewNone(), err
	}
	return NewBool(strings.Contains(args[0].AsString(), args[1].AsString())), nil
}

func fnReplace(args []Value) (Value, error) {
	if err := checkArity("replace", args, 3); err != nil {
		return NewNone(), err
	}
	return NewString(strings.ReplaceAll(args[0].AsString(), args[1].AsString(), args[2].AsString())), nil
}

// fnDefault is the functional form of ${x:-default}.
func fnDefault(args []Value) (Value, error) {
	if err := checkArity("default", args, 2); err != nil {
		return NewNone(), err
	}
	if args[0].IsEmpty() {
		return args[1], nil
	}
	return args[0], nil
}

// fnCoalesce returns the first non-empty argument, or None if all are empty.
func fnCoalesce(args []Value) (Value, error) {
	for _, a := range args {
		if !a.IsEmpty() {
			return a, nil
		}
	}
	return NewNone(), nil
}

// fnJoin joins a list value with a separator.
//
// In Stage 3a, list values are rare (mostly come from Stage 3b's !files).
// We accept either join(sep, list) or join(sep, str, str, ...) for
// ergonomics. Pipelines pass the value as first arg, so for
// `${list | join(", ")}` the separator is args[1] — we detect the order by
// checking which arg looks like a list.
func fnJoin(args []Value) (Value, error) {
	if len(args) < 1 {
		return NewNone(), fmt.Errorf("join expects at least 1 arg, got 0")
	}
	// Find the list-ish argument; everything else is treated as a separator
	// when there's exactly one non-list arg.
	var sep string
	var items []Value
	switch {
	case len(args) == 2 && args[1].Kind == ListKind:
		sep = args[0].AsString()
		items = args[1].List
	case len(args) == 2 && args[0].Kind == ListKind:
		// Pipeline form: list piped in first, sep is args[1].
		items = args[0].List
		sep = args[1].AsString()
	case args[0].Kind == ListKind:
		// One arg, a list: join with empty separator.
		items = args[0].List
	default:
		// Treat first as sep, rest as items.
		sep = args[0].AsString()
		items = args[1:]
	}
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.AsString()
	}
	return NewString(strings.Join(parts, sep)), nil
}
