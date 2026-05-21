// Package expr is the gmk expression language: AST, parser, and evaluator.
//
// Stage 3a supports:
//   - String/int/float/bool literals
//   - Var references: ${name}
//   - Typed refs: ${env:NAME}, ${ctx:NAME}, ${var:NAME}
//   - Modifiers (bash-compatible): ${name:-default}, ${name:?required}, ${name:+alternate}
//   - Function calls: upper(x), starts_with(s, prefix)
//   - Pipelines: ${x | upper | trim}
//   - Binary ops: ==, !=
//   - Parenthesized sub-expressions
//   - Template strings mixing literal text and ${...} substitutions
//
// Stage 3b will add YAML tags (!sh, !env, !files, !join) layered on top.
// Stage 4 will extend with && || comparisons and richer conditions.
// Stage 5 will add lazy evaluation markers. Stage 7 will add ${probe:...}.
//
// The package has no dependency on other gmk packages — it operates over
// an interface (VarResolver) that callers implement to plug in scope
// walking. This keeps the language layer reusable across stages and
// testable in isolation.
package expr

import (
	"fmt"
	"strconv"
)

// ValueKind tags the runtime type of a Value.
type ValueKind int

const (
	NoneKind   ValueKind = iota // unset / null
	StringKind                  // text
	IntKind                     // int64
	FloatKind                   // float64
	BoolKind                    // true/false
	ListKind                    // []Value (Stage 3a: only from !files; usage limited)
)

// String returns the human-readable name of a ValueKind for error messages.
func (k ValueKind) String() string {
	switch k {
	case NoneKind:
		return "none"
	case StringKind:
		return "string"
	case IntKind:
		return "int"
	case FloatKind:
		return "float"
	case BoolKind:
		return "bool"
	case ListKind:
		return "list"
	default:
		return "unknown"
	}
}

// Value is a runtime value in the expression language. It's a sum type
// realized as a struct with a Kind discriminator — chosen over interface{}
// because:
//   - typed access is explicit (no type assertions in eval paths)
//   - zero allocations for non-list values
//   - serialization to/from msgpack (Stage 6) is straightforward
//
// All accessor methods (As*) perform type coercion where defined; calling
// e.g. AsInt on a StringValue tries to parse the string. Unrepresentable
// coercions return errors.
type Value struct {
	Kind ValueKind
	Str  string
	Int  int64
	Flt  float64
	Bool bool
	List []Value
}

// NewNone returns a None value.
func NewNone() Value { return Value{Kind: NoneKind} }

// NewString returns a String value.
func NewString(s string) Value { return Value{Kind: StringKind, Str: s} }

// NewInt returns an Int value.
func NewInt(i int64) Value { return Value{Kind: IntKind, Int: i} }

// NewFloat returns a Float value.
func NewFloat(f float64) Value { return Value{Kind: FloatKind, Flt: f} }

// NewBool returns a Bool value.
func NewBool(b bool) Value { return Value{Kind: BoolKind, Bool: b} }

// NewList returns a List value backed by the given slice (not copied).
func NewList(items []Value) Value { return Value{Kind: ListKind, List: items} }

// AsString returns the value rendered as a string. All Values are
// renderable as strings; this never returns an error.
//
//	None  -> ""
//	String -> the string
//	Int   -> decimal form
//	Float -> Go-default float format
//	Bool  -> "true" / "false"
//	List  -> "[a, b, c]"  (Stage 3a; Stage 4 may parameterize)
func (v Value) AsString() string {
	switch v.Kind {
	case NoneKind:
		return ""
	case StringKind:
		return v.Str
	case IntKind:
		return strconv.FormatInt(v.Int, 10)
	case FloatKind:
		return strconv.FormatFloat(v.Flt, 'g', -1, 64)
	case BoolKind:
		if v.Bool {
			return "true"
		}
		return "false"
	case ListKind:
		out := "["
		for i, item := range v.List {
			if i > 0 {
				out += ", "
			}
			out += item.AsString()
		}
		out += "]"
		return out
	default:
		return ""
	}
}

// AsInt coerces the value to int64. Returns an error if coercion isn't
// possible (e.g. a string that doesn't parse as int).
func (v Value) AsInt() (int64, error) {
	switch v.Kind {
	case IntKind:
		return v.Int, nil
	case FloatKind:
		return int64(v.Flt), nil
	case BoolKind:
		if v.Bool {
			return 1, nil
		}
		return 0, nil
	case StringKind:
		n, err := strconv.ParseInt(v.Str, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot convert %q to int: %w", v.Str, err)
		}
		return n, nil
	case NoneKind:
		return 0, fmt.Errorf("cannot convert none to int")
	case ListKind:
		return 0, fmt.Errorf("cannot convert list to int")
	default:
		return 0, fmt.Errorf("cannot convert %s to int", v.Kind)
	}
}

// AsBool coerces the value to bool using gmk's truthiness rules:
//
//	Bool      -> the value
//	None      -> false
//	String    -> false if "", "0", "false", "no", "off" (case-insensitive); true otherwise
//	Int/Float -> non-zero is true
//	List      -> non-empty is true
//
// This mirrors common shell semantics, important for Stage 4 conditions.
func (v Value) AsBool() bool {
	switch v.Kind {
	case BoolKind:
		return v.Bool
	case NoneKind:
		return false
	case StringKind:
		if v.Str == "" {
			return false
		}
		switch lowercase(v.Str) {
		case "0", "false", "no", "off":
			return false
		}
		return true
	case IntKind:
		return v.Int != 0
	case FloatKind:
		return v.Flt != 0
	case ListKind:
		return len(v.List) > 0
	default:
		return false
	}
}

// IsEmpty reports whether the value is "empty" for modifier purposes:
//   - None is empty
//   - "" is empty
//   - Empty list is empty
//   - All other values are non-empty (including 0, false)
//
// Bash's ${var:-default} triggers on "var is unset or empty". This mirrors
// that exactly: unset vars resolve to None (empty); empty strings are also
// empty; zero and false are NOT empty (they're explicit values).
func (v Value) IsEmpty() bool {
	switch v.Kind {
	case NoneKind:
		return true
	case StringKind:
		return v.Str == ""
	case ListKind:
		return len(v.List) == 0
	default:
		return false
	}
}

// Equal reports whether two values are equal under expression-language
// semantics. Cross-kind comparisons coerce both sides to strings.
//
// Note that StringKind "1" == IntKind 1 is true (string coerces both
// sides via AsString -> "1" == "1"). This is intentional for YAML
// ergonomics — users write quoted strings in YAML but mean numbers often.
func (v Value) Equal(other Value) bool {
	if v.Kind == other.Kind {
		switch v.Kind {
		case StringKind:
			return v.Str == other.Str
		case IntKind:
			return v.Int == other.Int
		case FloatKind:
			return v.Flt == other.Flt
		case BoolKind:
			return v.Bool == other.Bool
		case NoneKind:
			return true
		case ListKind:
			if len(v.List) != len(other.List) {
				return false
			}
			for i := range v.List {
				if !v.List[i].Equal(other.List[i]) {
					return false
				}
			}
			return true
		}
	}
	return v.AsString() == other.AsString()
}

// lowercase is a small helper that doesn't pull in the strings package
// for one operation. ASCII-only, sufficient for the keywords in AsBool.
func lowercase(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
